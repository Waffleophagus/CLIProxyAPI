package tailnet

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"
)

// Listener names passed to ServeFunc.
const (
	ListenerHTTPS = "tailnet-https"
	ListenerHTTP  = "tailnet-http"
)

// debugRegisterEnv makes tailscale's control client dump registration
// requests, which carry the auth key, to the backend log.
const debugRegisterEnv = "TS_DEBUG_REGISTER"

// disableLogUpload turns off tailscale's logtail for the whole process. tsnet
// always creates a logtail logger that buffers backend logs under the state
// dir and uploads them to log.tailscale.com; it is fed independently of
// Server.Logf/UserLogf, so it would bypass logSink's redaction. Tests replace it.
var disableLogUpload = logtail.Disable

// hardenLogging must run before the tsnet server starts.
func hardenLogging() {
	disableLogUpload()
	if os.Getenv(debugRegisterEnv) != "" {
		log.Warnf("tailscale: ignoring %s; it would log registration requests including the auth key", debugRegisterEnv)
		envknob.Setenv(debugRegisterEnv, "")
	}
}

// ServeFunc serves the proxy handler on a tailnet listener until the listener
// is closed. It must mark requests as arriving via the tailnet.
type ServeFunc func(ln net.Listener, name string)

// Node is the embedded tsnet node.
type Node struct {
	settings Settings
	srv      *tsnet.Server
	logs     *logSink
	whois    *WhoIsCache

	// startServer, upServer and closeServer wrap srv; tests replace them to
	// drive startup and shutdown without a real tsnet node.
	startServer func() error
	upServer    func(context.Context) (*ipnstate.Status, error)
	closeServer func() error

	mu      sync.Mutex
	started bool // tsnet.Server.Start succeeded
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{} // closed when the startup goroutine returns
	serving sync.WaitGroup

	shutdownOnce sync.Once
	shutdownErr  error
}

// New creates a node from resolved settings. It does not touch the network.
func New(settings Settings) *Node {
	logs := &logSink{}
	n := &Node{
		settings: settings,
		logs:     logs,
		srv: &tsnet.Server{
			Dir:       settings.StateDir,
			Hostname:  settings.Hostname,
			AuthKey:   settings.AuthKey,
			Ephemeral: settings.Ephemeral,
			Logf:      logs.backendLogf,
			UserLogf:  logs.userLogf,
		},
	}
	n.startServer = n.srv.Start
	n.upServer = n.srv.Up
	n.closeServer = n.srv.Close
	n.whois = NewWhoIsCache(n.lookupWhoIs, DefaultWhoIsCacheTTL, nil)
	return n
}

// Settings returns the settings the node was created with.
func (n *Node) Settings() Settings {
	if n == nil {
		return Settings{}
	}
	return n.settings
}

// Start brings the node up in the background so the LAN listener is never
// blocked by tailnet login. Each tailnet listener is handed to serve in its
// own goroutine. Start may be called once.
func (n *Node) Start(ctx context.Context, serve ServeFunc) {
	if n == nil || serve == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n.mu.Lock()
	if n.done != nil || n.closed {
		n.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	n.cancel = cancel
	n.done = make(chan struct{})
	n.mu.Unlock()

	hardenLogging()
	go n.run(runCtx, serve)
}

func (n *Node) run(ctx context.Context, serve ServeFunc) {
	defer close(n.done)
	// A Close that gave up waiting for startup leaves the cleanup to us.
	defer func() {
		n.mu.Lock()
		closed := n.closed
		n.mu.Unlock()
		if closed {
			if errShutdown := n.shutdown(); errShutdown != nil {
				log.Warnf("tailscale: %v", errShutdown)
			}
		}
	}()
	log.Infof("tailscale: starting node %q (state dir %s)", n.settings.Hostname, n.settings.StateDir)
	if errStart := n.startServer(); errStart != nil {
		log.Errorf("tailscale: failed to start node: %v", errStart)
		return
	}
	n.mu.Lock()
	n.started = true
	n.mu.Unlock()

	if n.settings.HTTPS {
		if ln, errListen := n.listenTLS(); errListen != nil {
			log.Errorf("tailscale: failed to listen on tailnet :443: %v", errListen)
		} else {
			n.serve(serve, ln, ListenerHTTPS)
		}
	}
	if n.settings.HTTPPort > 0 {
		if ln, errListen := n.srv.Listen("tcp", fmt.Sprintf(":%d", n.settings.HTTPPort)); errListen != nil {
			log.Errorf("tailscale: failed to listen on tailnet :%d: %v", n.settings.HTTPPort, errListen)
		} else {
			n.serve(serve, ln, ListenerHTTP)
		}
	}

	// Up blocks until the node is Running (after login) or ctx ends.
	st, errUp := n.upServer(ctx)
	if errUp != nil {
		if ctx.Err() == nil {
			log.Warnf("tailscale: node did not come up: %v", errUp)
		}
		return
	}
	n.logRunning(st)
}

// listenTLS listens on tailnet :443 and terminates TLS with the node's
// auto-provisioned *.ts.net certificate. Unlike tsnet.ListenTLS it does not
// wait for login, so the listener is ready as soon as the node is.
func (n *Node) listenTLS() (net.Listener, error) {
	lc, errClient := n.srv.LocalClient()
	if errClient != nil {
		return nil, errClient
	}
	ln, errListen := n.srv.Listen("tcp", ":443")
	if errListen != nil {
		return nil, errListen
	}
	return tls.NewListener(ln, &tls.Config{
		GetCertificate: lc.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}), nil
}

func (n *Node) serve(serve ServeFunc, ln net.Listener, name string) {
	n.serving.Add(1)
	go func() {
		defer n.serving.Done()
		serve(ln, name)
	}()
}

func (n *Node) logRunning(st *ipnstate.Status) {
	status := statusFromIPN(st, n.settings.Hostname)
	log.Infof("tailscale: running as %s (%s)", status.DNSName, strings.Join(status.IPs, ", "))
	switch {
	case n.settings.HTTPS && len(status.CertDomains) == 0:
		log.Warn("tailscale: HTTPS certificates are unavailable; enable MagicDNS and HTTPS in the tailnet admin console (DNS page). TLS handshakes fail until then.")
	case n.settings.HTTPS:
		log.Infof("tailscale: serving https://%s/", status.DNSName)
	}
	if n.settings.HTTPPort > 0 && status.DNSName != "" {
		log.Infof("tailscale: serving http://%s:%d/", status.DNSName, n.settings.HTTPPort)
	}
}

// Close stops the node and its listeners and waits for serve goroutines. If
// startup is still in progress, it waits for it until ctx ends; when ctx ends
// first, the startup goroutine closes the node as soon as startup finishes.
func (n *Node) Close(ctx context.Context) error {
	if n == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	cancel, done := n.cancel, n.done
	n.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	// tsnet.Server.Close must not run concurrently with Start.
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("tailscale: node still starting at shutdown; it will be closed once startup finishes: %w", ctx.Err())
	}
	return n.shutdown()
}

// shutdown closes the tsnet server once, after startup has finished. Close
// calls it, or the startup goroutine does when Close stopped waiting.
func (n *Node) shutdown() error {
	n.shutdownOnce.Do(func() {
		n.mu.Lock()
		started := n.started
		n.started = false
		n.mu.Unlock()
		if !started {
			return
		}
		errClose := n.closeServer()
		n.serving.Wait()
		if errClose != nil {
			n.shutdownErr = fmt.Errorf("tailscale: close node: %w", errClose)
			return
		}
		log.Info("tailscale: node stopped")
	})
	return n.shutdownErr
}

func (n *Node) localClient() (*local.Client, error) {
	n.mu.Lock()
	started := n.started
	n.mu.Unlock()
	if !started {
		return nil, ErrNotRunning
	}
	return n.srv.LocalClient()
}

// WhoIs returns the tailnet identity behind remoteAddr ("ip:port"), cached briefly.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (Peer, error) {
	if n == nil {
		return Peer{}, ErrNotRunning
	}
	return n.whois.WhoIs(ctx, remoteAddr)
}

func (n *Node) lookupWhoIs(ctx context.Context, remoteAddr string) (Peer, error) {
	lc, errClient := n.localClient()
	if errClient != nil {
		return Peer{}, errClient
	}
	res, errWhoIs := lc.WhoIs(ctx, remoteAddr)
	if errWhoIs != nil {
		return Peer{}, errWhoIs
	}
	var peer Peer
	if res.Node != nil {
		peer.Tagged = res.Node.IsTagged()
		peer.NodeName = strings.TrimSuffix(res.Node.Name, ".")
	}
	if res.UserProfile != nil && !peer.Tagged {
		peer.LoginName = res.UserProfile.LoginName
		peer.DisplayName = res.UserProfile.DisplayName
	}
	return peer, nil
}

// Status reports the node state. Before startup completes it reports NoState.
func (n *Node) Status(ctx context.Context) (Status, error) {
	if n == nil {
		return Status{}, ErrNotRunning
	}
	lc, errClient := n.localClient()
	if errClient != nil {
		return Status{State: "NoState", Hostname: n.settings.Hostname}, nil
	}
	st, errStatus := lc.StatusWithoutPeers(ctx)
	if errStatus != nil {
		return Status{}, errStatus
	}
	return statusFromIPN(st, n.settings.Hostname), nil
}

func statusFromIPN(st *ipnstate.Status, hostname string) Status {
	out := Status{Hostname: hostname}
	if st == nil {
		return out
	}
	out.State = st.BackendState
	out.AuthURL = st.AuthURL
	out.MagicDNSSuffix = st.MagicDNSSuffix
	out.CertDomains = append([]string(nil), st.CertDomains...)
	for _, ip := range st.TailscaleIPs {
		out.IPs = append(out.IPs, ip.String())
	}
	if st.CurrentTailnet != nil {
		out.Tailnet = st.CurrentTailnet.Name
		out.MagicDNSEnabled = st.CurrentTailnet.MagicDNSEnabled
	}
	if self := st.Self; self != nil {
		out.DNSName = strings.TrimSuffix(self.DNSName, ".")
		if self.Tags != nil {
			out.Tags = self.Tags.AsSlice()
		}
		if len(out.Tags) == 0 {
			if profile, ok := st.User[self.UserID]; ok {
				out.SelfUser = profile.LoginName
			}
		}
	}
	return out
}
