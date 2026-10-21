package tailnet

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/control/controlclient"
	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnstate"
)

// controlledNode returns a node whose tsnet calls are replaced: Start blocks
// until release is closed, Up blocks until its context ends, and Close counts
// calls and signals closed on the first one.
func controlledNode(t *testing.T) (n *Node, started, release, closed chan struct{}, closes *atomic.Int32) {
	t.Helper()
	n = New(Settings{Enabled: true, Hostname: "cliproxy", StateDir: t.TempDir()})
	started = make(chan struct{})
	release = make(chan struct{})
	closed = make(chan struct{})
	closes = &atomic.Int32{}
	n.startServer = func() error {
		close(started)
		<-release
		return nil
	}
	n.upServer = func(ctx context.Context) (*ipnstate.Status, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	n.closeServer = func() error {
		if closes.Add(1) == 1 {
			close(closed)
		}
		return nil
	}
	return n, started, release, closed, closes
}

func noopServe(net.Listener, string) {}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNodeCanceledCloseStillClosesServerAfterStartup(t *testing.T) {
	n, started, release, closed, closes := controlledNode(t)
	n.Start(context.Background(), noopServe)
	waitClosed(t, started, "startup to begin")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if errClose := n.Close(ctx); errClose == nil {
		t.Fatal("Close with a canceled context during startup returned nil, want error")
	}
	if got := closes.Load(); got != 0 {
		t.Fatalf("server closed %d times while Start was still running", got)
	}

	// Startup finishes after the caller gave up: the node must still be closed.
	close(release)
	waitClosed(t, closed, "the tsnet server to be closed after startup finished")
	waitClosed(t, n.done, "the startup goroutine to return")

	if errClose := n.Close(context.Background()); errClose != nil {
		t.Fatalf("second Close = %v", errClose)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("server closed %d times, want 1", got)
	}
}

func TestNodeCloseWaitsForStartupThenClosesOnce(t *testing.T) {
	n, started, release, closed, closes := controlledNode(t)
	n.Start(context.Background(), noopServe)
	waitClosed(t, started, "startup to begin")

	result := make(chan error, 1)
	go func() { result <- n.Close(context.Background()) }()
	close(release)
	select {
	case errClose := <-result:
		if errClose != nil {
			t.Fatalf("Close = %v", errClose)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after startup finished")
	}
	waitClosed(t, closed, "the tsnet server to be closed")
	waitClosed(t, n.done, "the startup goroutine to return")
	if got := closes.Load(); got != 1 {
		t.Fatalf("server closed %d times, want 1", got)
	}
}

func TestNodeCloseAfterFailedStartDoesNotCloseServer(t *testing.T) {
	n, _, release, _, closes := controlledNode(t)
	n.startServer = func() error {
		<-release
		return errors.New("start failed")
	}
	n.Start(context.Background(), noopServe)
	close(release)
	waitClosed(t, n.done, "the startup goroutine to return")
	if errClose := n.Close(context.Background()); errClose != nil {
		t.Fatalf("Close = %v", errClose)
	}
	if got := closes.Load(); got != 0 {
		t.Fatalf("server closed %d times after a failed start, want 0", got)
	}
}

func TestNodeStartDisablesLogUploadBeforeStartingServer(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	prev := disableLogUpload
	disableLogUpload = func() { record("disable-log-upload") }
	t.Cleanup(func() { disableLogUpload = prev })

	n := New(Settings{Enabled: true, Hostname: "cliproxy", StateDir: t.TempDir()})
	n.startServer = func() error {
		record("start")
		return errors.New("stop here")
	}
	n.Start(context.Background(), noopServe)
	waitClosed(t, n.done, "the startup goroutine to return")

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "disable-log-upload" || order[1] != "start" {
		t.Fatalf("startup order = %v, want [disable-log-upload start]", order)
	}
}

func TestNodeStartIgnoresDebugRegisterDump(t *testing.T) {
	envknob.SetenvForTest(t, debugRegisterEnv, "true")
	if !controlclient.DevKnob.DumpRegister() {
		t.Fatal("test setup: register dump knob not enabled")
	}
	prev := disableLogUpload
	disableLogUpload = func() {}
	t.Cleanup(func() { disableLogUpload = prev })

	n := New(Settings{Enabled: true, Hostname: "cliproxy", StateDir: t.TempDir()})
	n.startServer = func() error { return errors.New("stop here") }
	n.Start(context.Background(), noopServe)
	waitClosed(t, n.done, "the startup goroutine to return")

	if controlclient.DevKnob.DumpRegister() {
		t.Fatalf("%s still enabled after Start; registration requests (with the auth key) would be logged", debugRegisterEnv)
	}
}
