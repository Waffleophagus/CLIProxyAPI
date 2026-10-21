package config

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultTailscaleHostname is the tailnet machine name used when none is configured.
const DefaultTailscaleHostname = "cliproxy"

// TailscaleConfig configures the embedded tsnet node (top-level "tailscale").
// Except for ManagementUsers, changes take effect after a restart.
type TailscaleConfig struct {
	// Enabled starts the tsnet node.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Hostname is the tailnet machine name (default "cliproxy").
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	// AuthKey joins the tailnet without interactive login. Empty falls back to
	// the TS_AUTHKEY environment variable, then to an interactive login URL.
	// It is a secret: never logged, and redacted from management JSON reads.
	AuthKey string `yaml:"auth-key,omitempty" json:"-"`
	// StateDir holds the node state (including its private key). Empty means
	// <data dir>/tailscale; relative paths resolve against the config directory.
	StateDir string `yaml:"state-dir,omitempty" json:"state-dir,omitempty"`
	// Ephemeral registers the node as ephemeral (removed when it goes offline).
	Ephemeral bool `yaml:"ephemeral,omitempty" json:"ephemeral,omitempty"`
	// HTTPS serves on tailnet port 443 with the auto-provisioned *.ts.net
	// certificate; nil means true.
	HTTPS *bool `yaml:"https,omitempty" json:"https,omitempty"`
	// HTTPPort additionally serves plain HTTP on this tailnet port (0 = off).
	// When HTTPS is off and HTTPPort is 0, the main server port is used.
	HTTPPort int `yaml:"http-port,omitempty" json:"http-port,omitempty"`
	// ManagementUsers lists tailnet login names (e.g. "user@github") that may use
	// /v8/management over the tailnet without the management key.
	ManagementUsers []string `yaml:"management-users,omitempty" json:"management-users,omitempty"`
}

// HTTPSEnabled reports whether the tailnet HTTPS listener is enabled (default true).
func (c TailscaleConfig) HTTPSEnabled() bool {
	return c.HTTPS == nil || *c.HTTPS
}

// EffectiveHostname returns the configured hostname or the default.
func (c TailscaleConfig) EffectiveHostname() string {
	if hostname := strings.TrimSpace(c.Hostname); hostname != "" {
		return hostname
	}
	return DefaultTailscaleHostname
}

// IsManagementUser reports whether loginName is listed in ManagementUsers
// (case-insensitive). An empty login name never matches.
func (c TailscaleConfig) IsManagementUser(loginName string) bool {
	loginName = strings.TrimSpace(loginName)
	if loginName == "" {
		return false
	}
	for _, user := range c.ManagementUsers {
		if strings.EqualFold(strings.TrimSpace(user), loginName) {
			return true
		}
	}
	return false
}

var tailscaleHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidateTailscale rejects malformed tailscale settings.
func (cfg *Config) ValidateTailscale() error {
	if cfg == nil {
		return nil
	}
	ts := cfg.Tailscale
	if hostname := strings.TrimSpace(ts.Hostname); hostname != "" && !tailscaleHostnamePattern.MatchString(hostname) {
		return fmt.Errorf("tailscale.hostname %q must be a DNS label (letters, digits and hyphens, at most 63 characters)", hostname)
	}
	if ts.HTTPPort < 0 || ts.HTTPPort > 65535 {
		return fmt.Errorf("tailscale.http-port %d must be between 0 and 65535", ts.HTTPPort)
	}
	if ts.HTTPPort == 443 && ts.HTTPSEnabled() {
		return fmt.Errorf("tailscale.http-port 443 conflicts with the tailnet HTTPS listener; disable tailscale.https or pick another port")
	}
	for i, user := range ts.ManagementUsers {
		if strings.TrimSpace(user) == "" {
			return fmt.Errorf("tailscale.management-users[%d] must not be empty", i)
		}
	}
	return nil
}
