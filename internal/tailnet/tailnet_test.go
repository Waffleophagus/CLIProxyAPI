package tailnet

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

func TestResolveSettingsDefaults(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv(AuthKeyEnv, "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Port: 8317, Tailscale: config.TailscaleConfig{Enabled: true}}

	got := ResolveSettings(cfg, configPath)
	want := Settings{Enabled: true, Hostname: "cliproxy", StateDir: filepath.Join(dir, "data", "tailscale"), HTTPS: true}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}

	t.Setenv("WRITABLE_PATH", "/srv/cliproxy")
	t.Setenv(AuthKeyEnv, "tskey-auth-from-env")
	off := false
	cfg.Tailscale = config.TailscaleConfig{Enabled: true, Hostname: "proxy", HTTPS: &off}
	got = ResolveSettings(cfg, configPath)
	if got.StateDir != filepath.Join("/srv/cliproxy", "data", "tailscale") {
		t.Fatalf("state dir = %q", got.StateDir)
	}
	if got.AuthKey != "tskey-auth-from-env" || got.Hostname != "proxy" || got.HTTPS {
		t.Fatalf("settings = %+v", got)
	}
	if got.HTTPPort != 8317 {
		t.Fatalf("http port = %d, want main port when https is off", got.HTTPPort)
	}

	cfg.Tailscale = config.TailscaleConfig{Enabled: true, AuthKey: "tskey-auth-config", StateDir: "ts-state", HTTPPort: 8080}
	got = ResolveSettings(cfg, configPath)
	if got.AuthKey != "tskey-auth-config" || got.StateDir != filepath.Join(dir, "ts-state") || got.HTTPPort != 8080 || !got.HTTPS {
		t.Fatalf("settings = %+v", got)
	}
}

func TestWhoIsCacheTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	failNext := false
	cache := NewWhoIsCache(func(_ context.Context, addr string) (Peer, error) {
		calls++
		if failNext {
			return Peer{}, errors.New("boom")
		}
		return Peer{LoginName: addr}, nil
	}, 10*time.Second, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		peer, errWhoIs := cache.WhoIs(context.Background(), "100.64.0.1:1")
		if errWhoIs != nil || peer.LoginName != "100.64.0.1:1" {
			t.Fatalf("peer=%+v err=%v", peer, errWhoIs)
		}
	}
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1 while cached", calls)
	}
	if _, errWhoIs := cache.WhoIs(context.Background(), "100.64.0.2:1"); errWhoIs != nil || calls != 2 {
		t.Fatalf("distinct address should be looked up: calls=%d err=%v", calls, errWhoIs)
	}

	now = now.Add(10 * time.Second)
	failNext = true
	if _, errWhoIs := cache.WhoIs(context.Background(), "100.64.0.1:1"); errWhoIs == nil || calls != 3 {
		t.Fatalf("expired entry should be looked up again: calls=%d err=%v", calls, errWhoIs)
	}
	if _, errWhoIs := cache.WhoIs(context.Background(), "100.64.0.1:1"); errWhoIs == nil || calls != 4 {
		t.Fatalf("errors must not be cached: calls=%d err=%v", calls, errWhoIs)
	}
}

func TestLogSinkRedactsSecretsAndAnnouncesLoginURLOnce(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	prevLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(prevLevel)

	sink := &logSink{}
	sink.backendLogf("control: using key %s for %s", "tskey-auth-kAbC123-SECRET", "https://login.tailscale.com/a/0123abcd")
	sink.userLogf("To start this tsnet server, restart with TS_AUTHKEY set, or go to: %s", "https://login.tailscale.com/a/0123abcd")
	sink.userLogf("To start this tsnet server, restart with TS_AUTHKEY set, or go to: %s", "https://login.tailscale.com/a/0123abcd")
	sink.userLogf("tsnet starting with hostname %q", "cliproxy")

	var warns []string
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "SECRET") {
			t.Fatalf("auth key leaked: %q", entry.Message)
		}
		if entry.Level == log.DebugLevel && strings.Contains(entry.Message, "/a/0123abcd") {
			t.Fatalf("login URL leaked at debug: %q", entry.Message)
		}
		if entry.Level == log.WarnLevel {
			warns = append(warns, entry.Message)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "https://login.tailscale.com/a/0123abcd") {
		t.Fatalf("warnings = %q, want the login URL exactly once", warns)
	}
}

func TestStatusFromIPN(t *testing.T) {
	st := &ipnstate.Status{
		BackendState:   "Running",
		TailscaleIPs:   []netip.Addr{netip.MustParseAddr("100.100.1.2")},
		CertDomains:    []string{"cliproxy.tail1234.ts.net"},
		MagicDNSSuffix: "tail1234.ts.net",
		CurrentTailnet: &ipnstate.TailnetStatus{Name: "matt@github", MagicDNSEnabled: true},
		Self:           &ipnstate.PeerStatus{DNSName: "cliproxy.tail1234.ts.net.", UserID: 7},
		User:           map[tailcfg.UserID]tailcfg.UserProfile{7: {LoginName: "matt@github"}},
	}
	got := statusFromIPN(st, "cliproxy")
	if got.State != "Running" || got.DNSName != "cliproxy.tail1234.ts.net" || got.SelfUser != "matt@github" || got.Tailnet != "matt@github" || len(got.IPs) != 1 || !got.MagicDNSEnabled {
		t.Fatalf("status = %+v", got)
	}

	tags := views.SliceOf([]string{"tag:proxy"})
	st.Self.Tags = &tags
	if got = statusFromIPN(st, "cliproxy"); got.SelfUser != "" || len(got.Tags) != 1 {
		t.Fatalf("tagged node status = %+v", got)
	}
}

func TestNodeCloseWithoutStart(t *testing.T) {
	n := New(Settings{Enabled: true, Hostname: "cliproxy", StateDir: t.TempDir()})
	if errClose := n.Close(context.Background()); errClose != nil {
		t.Fatal(errClose)
	}
	if _, errWhoIs := n.WhoIs(context.Background(), "100.64.0.1:1"); !errors.Is(errWhoIs, ErrNotRunning) {
		t.Fatalf("whois before start = %v, want ErrNotRunning", errWhoIs)
	}
	st, errStatus := n.Status(context.Background())
	if errStatus != nil || st.State != "NoState" {
		t.Fatalf("status = %+v err=%v", st, errStatus)
	}
}

func TestViaTailnetContext(t *testing.T) {
	if ViaTailnet(context.Background()) {
		t.Fatal("plain context reported via tailnet")
	}
	if !ViaTailnet(WithViaTailnet(context.Background())) {
		t.Fatal("marked context not reported via tailnet")
	}
}
