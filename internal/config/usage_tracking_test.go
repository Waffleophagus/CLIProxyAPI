package config

import (
	"strings"
	"testing"
)

func TestUsageTrackingV8Layout(t *testing.T) {
	raw := `config-version: 8
observability:
  usage:
    enabled: false
    path: /var/lib/cpa/usage.db
    retention-days: 30
`
	if errValidate := ValidateV8Config([]byte(raw)); errValidate != nil {
		t.Fatalf("ValidateV8Config: %v", errValidate)
	}
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatal(errParse)
	}
	u := cfg.UsageTracking
	if u.IsEnabled() || u.Path != "/var/lib/cpa/usage.db" || u.EffectiveRetentionDays() != 30 {
		t.Fatalf("usage tracking = %+v", u)
	}

	out, _, errNormalize := NormalizeConfigLayout([]byte("usage-tracking:\n  retention-days: 7\n"), true)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	if !strings.Contains(string(out), "observability:\n    usage:\n        retention-days: 7") {
		t.Fatalf("legacy key was not migrated to observability.usage:\n%s", out)
	}
}

func TestUsageTrackingDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !cfg.UsageTracking.IsEnabled() || cfg.UsageTracking.EffectiveRetentionDays() != DefaultUsageRetentionDays {
		t.Fatalf("defaults = %+v", cfg.UsageTracking)
	}
	if (UsageTrackingConfig{RetentionDays: -1}).EffectiveRetentionDays() != 0 {
		t.Fatal("negative retention should disable pruning")
	}
}
