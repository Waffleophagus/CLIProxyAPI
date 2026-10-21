package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTailscaleConfigDefaults(t *testing.T) {
	var ts TailscaleConfig
	if !ts.HTTPSEnabled() || ts.EffectiveHostname() != DefaultTailscaleHostname {
		t.Fatalf("defaults: https=%t hostname=%q", ts.HTTPSEnabled(), ts.EffectiveHostname())
	}
	ts.ManagementUsers = []string{" Matt@GitHub "}
	if !ts.IsManagementUser("matt@github") || ts.IsManagementUser("") || ts.IsManagementUser("other@github") {
		t.Fatal("management user matching is wrong")
	}
}

func TestValidateTailscale(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name string
		ts   TailscaleConfig
		want string
	}{
		{"valid", TailscaleConfig{Enabled: true, Hostname: "cli-proxy", HTTPPort: 8080, ManagementUsers: []string{"matt@github"}}, ""},
		{"bad hostname", TailscaleConfig{Hostname: "cli.proxy"}, "tailscale.hostname"},
		{"port range", TailscaleConfig{HTTPPort: 70000}, "tailscale.http-port"},
		{"port conflict", TailscaleConfig{HTTPPort: 443}, "conflicts"},
		{"port 443 without https", TailscaleConfig{HTTPPort: 443, HTTPS: &off}, ""},
		{"empty user", TailscaleConfig{ManagementUsers: []string{" "}}, "management-users[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&Config{Tailscale: tc.ts}).ValidateTailscale()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadConfigTailscaleSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nserver:\n  port: 8317\ntailscale:\n  enabled: true\n  hostname: cliproxy\n  auth-key: tskey-auth-secret\n  https: false\n  http-port: 8080\n  management-users: [matt@github]\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := cfg.Tailscale
	if !ts.Enabled || ts.AuthKey != "tskey-auth-secret" || ts.HTTPSEnabled() || ts.HTTPPort != 8080 || !ts.IsManagementUser("MATT@github") {
		t.Fatalf("tailscale = %+v", ts)
	}
	if err = ValidateV8Config([]byte(raw)); err != nil {
		t.Fatalf("ValidateV8Config: %v", err)
	}
	if err = ValidateV8Config([]byte("config-version: 8\ntailscale:\n  hostname: bad.name\n")); err == nil {
		t.Fatal("ValidateV8Config accepted an invalid hostname")
	}

	if err = os.WriteFile(path, []byte("port: 8317\ntailscale:\n  http-port: 443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted conflicting tailscale ports")
	}
}

func TestTailscaleSectionDoesNotMarkLegacyConfigAsV8(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("port: 8317\ntailscale:\n  enabled: true\n"), &doc); err != nil {
		t.Fatal(err)
	}
	if IsV8ConfigLayout(doc.Content[0]) {
		t.Fatal("a legacy config with a tailscale section was detected as v8")
	}
}
