package cliproxy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestNoteTailscaleConfigWarnsOncePerChange(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	t.Setenv(tailnet.AuthKeyEnv, "")
	t.Setenv("WRITABLE_PATH", "")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	running := &config.Config{Port: 8317}
	running.Tailscale.Enabled = true
	s := &Service{configPath: configPath, tailnet: tailnet.New(tailnet.ResolveSettings(running, configPath))}

	restartWarnings := func() int {
		count := 0
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "restart required") {
				count++
			}
		}
		return count
	}

	// Management users apply live and never need a restart.
	same := *running
	same.Tailscale.ManagementUsers = []string{"matt@github"}
	s.noteTailscaleConfig(&same)
	if got := restartWarnings(); got != 0 {
		t.Fatalf("warnings = %d, want 0 for management-users only", got)
	}

	changed := same
	changed.Tailscale.Hostname = "other"
	s.noteTailscaleConfig(&changed)
	s.noteTailscaleConfig(&changed)
	if got := restartWarnings(); got != 1 {
		t.Fatalf("warnings = %d, want 1 for a repeated change", got)
	}

	// Reverting clears the note so a later change is reported again.
	s.noteTailscaleConfig(&same)
	s.noteTailscaleConfig(&changed)
	if got := restartWarnings(); got != 2 {
		t.Fatalf("warnings = %d, want 2", got)
	}

	// Enabling tailscale on a service started without it needs a restart too.
	off := &Service{configPath: configPath}
	off.noteTailscaleConfig(running)
	if got := restartWarnings(); got != 3 {
		t.Fatalf("warnings = %d, want 3", got)
	}
}
