package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const tailnetPeerAddr = "100.64.0.5:41641"

type fakeTailnetNode struct {
	mu       sync.Mutex
	peer     tailnet.Peer
	whoisErr error
	calls    []string
	status   tailnet.Status
	settings tailnet.Settings
}

func (f *fakeTailnetNode) WhoIs(_ context.Context, remoteAddr string) (tailnet.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, remoteAddr)
	if f.whoisErr != nil {
		return tailnet.Peer{}, f.whoisErr
	}
	return f.peer, nil
}

func (f *fakeTailnetNode) Status(context.Context) (tailnet.Status, error) {
	return f.status, nil
}

func (f *fakeTailnetNode) Settings() tailnet.Settings { return f.settings }

func (f *fakeTailnetNode) whoisCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newTailnetTestServer(t *testing.T, node *fakeTailnetNode, managementUsers ...string) *Server {
	t.Helper()
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		Tailscale: proxyconfig.TailscaleConfig{Enabled: true, ManagementUsers: managementUsers},
	}
	var opts []ServerOption
	if node != nil {
		opts = append(opts, WithTailnet(node))
	}
	return newTestServerWithConfig(t, cfg, opts...)
}

func serveRequest(s *Server, method, path string, viaTailnet bool, managementKey string) *httptest.ResponseRecorder {
	return serveTailnetRequest(s, method, path, viaTailnet, managementKey, nil)
}

// tailnetNodeHost is the Host test requests are addressed to: the node's
// tailnet IP, which passes the host check without a MagicDNS name.
const tailnetNodeHost = "100.100.1.2"

func serveTailnetRequest(s *Server, method, path string, viaTailnet bool, managementKey string, prepare func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = tailnetNodeHost
	req.RemoteAddr = tailnetPeerAddr
	if prepare != nil {
		prepare(req)
	}
	if viaTailnet {
		req = req.WithContext(tailnet.WithViaTailnet(req.Context()))
	}
	if managementKey != "" {
		req.Header.Set("Authorization", "Bearer "+managementKey)
	}
	rr := httptest.NewRecorder()
	s.engine.ServeHTTP(rr, req)
	return rr
}

const statusPath = "/v8/management/tailscale/status"

func TestTailnetIdentityAllowsManagementUserWithoutKey(t *testing.T) {
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "Matt@GitHub", NodeName: "laptop.tail.ts.net"}, status: tailnet.Status{State: "Running"}}
	s := newTailnetTestServer(t, node, "matt@github")

	rr := serveRequest(s, http.MethodGet, statusPath, true, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Caller struct {
			ViaTailnet         bool   `json:"via_tailnet"`
			LoginName          string `json:"login_name"`
			ManagementIdentity bool   `json:"management_identity"`
		} `json:"caller"`
	}
	if errDecode := json.Unmarshal(rr.Body.Bytes(), &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !body.Caller.ViaTailnet || body.Caller.LoginName != "Matt@GitHub" || !body.Caller.ManagementIdentity {
		t.Fatalf("caller = %+v", body.Caller)
	}
	if calls := node.whoisCalls(); len(calls) != 1 || calls[0] != tailnetPeerAddr {
		t.Fatalf("whois calls = %v, want one lookup of %s", calls, tailnetPeerAddr)
	}
}

func TestTailnetIdentityUnknownUserFallsBackToManagementKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "someone@github"}}
	s := newTailnetTestServer(t, node, "matt@github")

	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("without key: status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if rr := serveRequest(s, http.MethodGet, statusPath, true, "test-management-key"); rr.Code != http.StatusOK {
		t.Fatalf("with key: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestTailnetIdentityTaggedNodeIsNotAuthorized(t *testing.T) {
	// A tagged node may still carry a profile; it must never count as a user.
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github", Tagged: true}}
	s := newTailnetTestServer(t, node, "matt@github")
	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("no secret: status = %d, want 404", rr.Code)
	}

	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	s = newTailnetTestServer(t, node, "matt@github")
	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("with secret, no key: status = %d, want 401", rr.Code)
	}
}

func TestTailnetIdentityNeverAppliesToLANRequests(t *testing.T) {
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github"}}
	s := newTailnetTestServer(t, node, "matt@github")

	// Same remote address as an authorized tailnet peer, but accepted on the LAN listener.
	if rr := serveRequest(s, http.MethodGet, statusPath, false, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("no secret: status = %d, want 404", rr.Code)
	}
	if calls := node.whoisCalls(); len(calls) != 0 {
		t.Fatalf("LAN request triggered whois lookups: %v", calls)
	}

	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	s = newTailnetTestServer(t, node, "matt@github")
	if rr := serveRequest(s, http.MethodGet, statusPath, false, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("with secret, no key: status = %d, want 401", rr.Code)
	}
}

func TestTailnetIdentityNeverBypassesInferenceAuth(t *testing.T) {
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github"}}
	s := newTailnetTestServer(t, node, "matt@github")

	for _, viaTailnet := range []bool{true, false} {
		if rr := serveRequest(s, http.MethodGet, "/v1/models", viaTailnet, ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("viaTailnet=%t: /v1/models status = %d, want 401", viaTailnet, rr.Code)
		}
	}
	if calls := node.whoisCalls(); len(calls) != 0 {
		t.Fatalf("inference request triggered whois lookups: %v", calls)
	}
}

func TestTailnetManagementRoutesNeedUsersOrSecret(t *testing.T) {
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github"}}

	// No secret and no management users: routes are not registered at all.
	s := newTailnetTestServer(t, node)
	if s.managementRoutesRegistered.Load() {
		t.Fatal("management routes registered without secret or management users")
	}
	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}

	// Without a tailscale node, management users alone register nothing.
	s = newTailnetTestServer(t, nil, "matt@github")
	if s.managementRoutesRegistered.Load() {
		t.Fatal("management routes registered without a tailscale node")
	}

	// Management users hot-reloaded in: routes register, tailnet-only access.
	s = newTailnetTestServer(t, node)
	cfg := s.getConfig().CloneForRuntime()
	cfg.Tailscale.ManagementUsers = []string{"matt@github"}
	s.UpdateClients(cfg)
	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusOK {
		t.Fatalf("after reload via tailnet: status = %d, want 200", rr.Code)
	}
	if rr := serveRequest(s, http.MethodGet, statusPath, false, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("after reload via LAN: status = %d, want 404", rr.Code)
	}
	if rr := serveRequest(s, http.MethodGet, "/v8/management/oauth/callback", false, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("oauth callback via LAN: status = %d, want 404", rr.Code)
	}

	// Removing the user revokes identity access without a restart.
	cfg = cfg.CloneForRuntime()
	cfg.Tailscale.ManagementUsers = nil
	s.UpdateClients(cfg)
	if rr := serveRequest(s, http.MethodGet, statusPath, true, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("after removal via tailnet: status = %d, want 404", rr.Code)
	}
}

func TestTailscaleStatusEndpoint(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	settings := tailnet.Settings{Enabled: true, Hostname: "cliproxy", HTTPS: true}

	decode := func(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var body map[string]any
		if errDecode := json.Unmarshal(rr.Body.Bytes(), &body); errDecode != nil {
			t.Fatal(errDecode)
		}
		return body
	}

	t.Run("needs login", func(t *testing.T) {
		node := &fakeTailnetNode{settings: settings, status: tailnet.Status{State: "NeedsLogin", Hostname: "cliproxy", AuthURL: "https://login.tailscale.com/a/abc123"}}
		s := newTailnetTestServer(t, node)
		body := decode(t, serveRequest(s, http.MethodGet, statusPath, false, "test-management-key"))
		if body["state"] != "NeedsLogin" || body["auth_url"] != "https://login.tailscale.com/a/abc123" || body["active"] != true {
			t.Fatalf("body = %v", body)
		}
		caller, _ := body["caller"].(map[string]any)
		if caller["via_tailnet"] != false || caller["login_name"] != nil {
			t.Fatalf("caller = %v", caller)
		}
	})

	t.Run("running", func(t *testing.T) {
		node := &fakeTailnetNode{settings: settings, status: tailnet.Status{
			State: "Running", Hostname: "cliproxy", DNSName: "cliproxy.tail1234.ts.net", Tailnet: "matt@github",
			IPs: []string{"100.100.1.2"}, CertDomains: []string{"cliproxy.tail1234.ts.net"}, SelfUser: "matt@github",
			AuthURL: "https://login.tailscale.com/a/stale",
		}}
		s := newTailnetTestServer(t, node)
		node.settings = tailnet.ResolveSettings(s.getConfig(), s.configFilePath)
		body := decode(t, serveRequest(s, http.MethodGet, statusPath, false, "test-management-key"))
		if body["restart_required"] != false {
			t.Fatalf("restart_required = %v, want false for matching settings", body["restart_required"])
		}
		if body["state"] != "Running" || body["dns_name"] != "cliproxy.tail1234.ts.net" || body["self_user"] != "matt@github" || body["https_enabled"] != true {
			t.Fatalf("body = %v", body)
		}
		if _, ok := body["auth_url"]; ok {
			t.Fatalf("auth_url exposed while running: %v", body)
		}
		urls, _ := body["urls"].([]any)
		if len(urls) != 1 || urls[0] != "https://cliproxy.tail1234.ts.net" {
			t.Fatalf("urls = %v", body["urls"])
		}
	})

	t.Run("disabled", func(t *testing.T) {
		s := newTestServer(t)
		body := decode(t, serveRequest(s, http.MethodGet, statusPath, false, "test-management-key"))
		if body["state"] != "Disabled" || body["enabled"] != false || body["active"] != false || body["restart_required"] != false {
			t.Fatalf("body = %v", body)
		}
	})
}

func TestServeListenerMarksTailnetAndStopCloses(t *testing.T) {
	node := &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github"}, status: tailnet.Status{State: "Running"}}
	s := newTailnetTestServer(t, node, "matt@github")

	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	served := make(chan error, 1)
	go func() { served <- s.ServeListener(ln, ListenerOptions{Name: "test-tailnet", Tailnet: true}) }()

	base := "http://" + ln.Addr().String()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, errGet := client.Get(base + statusPath)
	if errGet != nil {
		t.Fatal(errGet)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		Caller struct {
			ViaTailnet bool   `json:"via_tailnet"`
			LoginName  string `json:"login_name"`
		} `json:"caller"`
	}
	if errDecode := json.Unmarshal(raw, &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !body.Caller.ViaTailnet || body.Caller.LoginName != "matt@github" {
		t.Fatalf("caller = %+v", body.Caller)
	}
	calls := node.whoisCalls()
	if len(calls) != 1 {
		t.Fatalf("whois calls = %v", calls)
	}
	if host, _, _ := net.SplitHostPort(calls[0]); host != "127.0.0.1" {
		t.Fatalf("whois looked up %q, want the connection's remote address", calls[0])
	}

	// The same handler on the main (LAN) path does not mark requests.
	if rr := serveRequest(s, http.MethodGet, statusPath, false, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("LAN status = %d, want 404", rr.Code)
	}

	if errStop := s.Stop(context.Background()); errStop != nil {
		t.Fatal(errStop)
	}
	select {
	case errServe := <-served:
		if errServe != nil {
			t.Fatalf("ServeListener returned %v, want nil after Stop", errServe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListener did not return after Stop")
	}
	if _, errGet = client.Get(base + "/healthz"); errGet == nil {
		t.Fatal("listener still accepting after Stop")
	}

	// Listeners handed over after Stop are closed immediately.
	late, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	if errServe := s.ServeListener(late, ListenerOptions{Name: "late", Tailnet: true}); errServe != nil {
		t.Fatalf("late ServeListener = %v", errServe)
	}
	if _, errAccept := late.Accept(); !errors.Is(errAccept, net.ErrClosed) {
		t.Fatalf("late listener Accept = %v, want net.ErrClosed", errAccept)
	}
}
