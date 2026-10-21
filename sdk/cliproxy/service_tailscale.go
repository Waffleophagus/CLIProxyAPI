package cliproxy

import (
	"context"
	"net"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// newTailnetNode creates the embedded tailscale node when tailscale.enabled is
// set. It does not touch the network.
func (s *Service) newTailnetNode() *tailnet.Node {
	settings := tailnet.ResolveSettings(s.currentSourceConfig(), s.configPath)
	if !settings.Enabled {
		return nil
	}
	return tailnet.New(settings)
}

// startTailnet brings the node up in the background and serves the API handler
// on its listeners; the LAN listener keeps serving meanwhile.
func (s *Service) startTailnet(ctx context.Context) {
	if s == nil || s.tailnet == nil || s.server == nil {
		return
	}
	server := s.server
	s.tailnet.Start(ctx, func(ln net.Listener, name string) {
		if errServe := server.ServeListener(ln, api.ListenerOptions{Name: name, Tailnet: true}); errServe != nil {
			log.Errorf("tailscale: %v", errServe)
		}
	})
}

// noteTailscaleConfig logs once per distinct change when the configured
// tailscale settings no longer match the running node; they apply on restart.
// Management users are read live and never need a restart.
// Callers must hold configRuntimeMu.
func (s *Service) noteTailscaleConfig(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	wanted := tailnet.ResolveSettings(cfg, s.configPath)
	active := s.tailnet.Settings()
	changed := wanted.Enabled != active.Enabled || (wanted.Enabled && wanted != active)
	if !changed {
		s.tailscaleRestartNoted = nil
		return
	}
	if s.tailscaleRestartNoted != nil && *s.tailscaleRestartNoted == wanted {
		return
	}
	s.tailscaleRestartNoted = &wanted
	log.Warn("tailscale settings changed; restart required to apply them")
}
