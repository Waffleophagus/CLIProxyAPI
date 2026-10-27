package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
	log "github.com/sirupsen/logrus"
)

// ListenerOptions describes an additional listener served with the main handler.
type ListenerOptions struct {
	// Name labels the listener in logs.
	Name string
	// Tailnet marks every request on the listener as arriving via the tailnet,
	// which enables WhoIs identity checks for management requests. Only set it
	// for listeners whose peers are authenticated by the tailnet.
	Tailnet bool
}

// ServeListener serves the API handler on an additional listener until Stop
// is called or the listener is closed. It blocks like Start; each listener
// gets its own http.Server so Stop can close it.
func (s *Server) ServeListener(ln net.Listener, opts ListenerOptions) error {
	if s == nil || ln == nil {
		return fmt.Errorf("serve listener: server or listener not initialized")
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: inboundReadHeaderTimeout}
	if opts.Tailnet {
		srv.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
			return tailnet.WithViaTailnet(ctx)
		}
	}

	s.extraMu.Lock()
	if s.stopped {
		s.extraMu.Unlock()
		if errClose := ln.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			log.Debugf("failed to close %s listener after stop: %v", opts.Name, errClose)
		}
		return nil
	}
	if s.extraServers == nil {
		s.extraServers = make(map[*http.Server]struct{})
	}
	s.extraServers[srv] = struct{}{}
	s.extraMu.Unlock()
	defer func() {
		s.extraMu.Lock()
		delete(s.extraServers, srv)
		s.extraMu.Unlock()
	}()

	log.Infof("API server listening on %s (%s)", ln.Addr(), opts.Name)
	errServe := srv.Serve(ln)
	if errServe == nil || errors.Is(errServe, http.ErrServerClosed) || errors.Is(errServe, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("serve %s listener: %w", opts.Name, errServe)
}

// closeExtraListeners marks the server stopped and closes every additional
// listener's http.Server.
func (s *Server) closeExtraListeners() error {
	s.extraMu.Lock()
	s.stopped = true
	servers := make([]*http.Server, 0, len(s.extraServers))
	for srv := range s.extraServers {
		servers = append(servers, srv)
	}
	s.extraMu.Unlock()

	var errs []error
	for _, srv := range servers {
		if errClose := srv.Close(); errClose != nil && !errors.Is(errClose, http.ErrServerClosed) && !errors.Is(errClose, net.ErrClosed) {
			errs = append(errs, errClose)
		}
	}
	return errors.Join(errs...)
}
