package helps

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func claudeDeviceHeaders(userAgent string) http.Header {
	return http.Header{
		"User-Agent":                  {userAgent},
		"X-Stainless-Package-Version": {defaultClaudeFingerprintPackageVersion},
		"X-Stainless-Runtime-Version": {defaultClaudeFingerprintRuntimeVersion},
		"X-Stainless-Os":              {"Windows"},
		"X-Stainless-Arch":            {"x64"},
	}
}

func TestResolveClaudeDeviceProfileLocalUsesBaselineForInvalidSignals(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-invalid-signals"}
	headers := claudeDeviceHeaders("claude-cli/999.0.0 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "999.0.0")
	headers.Set("X-Stainless-Runtime-Version", "v999.0.0")

	profile := resolveClaudeDeviceProfileLocal(auth, "api-key", headers, nil)
	baseline := defaultClaudeDeviceProfile(nil)
	if profile.UserAgent != baseline.UserAgent || profile.PackageVersion != baseline.PackageVersion || profile.RuntimeVersion != baseline.RuntimeVersion {
		t.Fatalf("invalid profile = %#v, want local baseline %#v", profile, baseline)
	}
}

func TestResolveClaudeDeviceProfileLocalKeepsExactMeasuredSoftwareBaseline(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-newer-patch-signals"}
	headers := claudeDeviceHeaders("claude-cli/2.1.281 (external, cli)")

	profile := resolveClaudeDeviceProfileLocal(auth, "api-key", headers, nil)
	baseline := defaultClaudeDeviceProfile(nil)
	if profile.UserAgent != baseline.UserAgent || profile.PackageVersion != baseline.PackageVersion || profile.RuntimeVersion != baseline.RuntimeVersion {
		t.Fatalf("unmeasured profile = %#v, want exact local baseline %#v", profile, baseline)
	}
}

func TestApplyClaudeLegacyDeviceHeadersReplacesInvalidNativeSoftwareSignals(t *testing.T) {
	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	incoming := claudeDeviceHeaders("claude-cli/999.0.0 (external, cli)")
	incoming.Set("X-Stainless-Package-Version", "999.0.0")
	incoming.Set("X-Stainless-Runtime-Version", "v999.0.0")

	ApplyClaudeLegacyDeviceHeaders(request, incoming, nil, true)

	baseline := defaultClaudeDeviceProfile(nil)
	if got := request.Header.Get("User-Agent"); got != baseline.UserAgent {
		t.Fatalf("User-Agent = %q, want local baseline %q", got, baseline.UserAgent)
	}
	if got := request.Header.Get("X-Stainless-Package-Version"); got != baseline.PackageVersion {
		t.Fatalf("X-Stainless-Package-Version = %q, want %q", got, baseline.PackageVersion)
	}
	if got := request.Header.Get("X-Stainless-Runtime-Version"); got != baseline.RuntimeVersion {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want %q", got, baseline.RuntimeVersion)
	}
}

func TestApplyClaudeLegacyDeviceHeadersAcceptsConfiguredMeasuredBaseline(t *testing.T) {
	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent:      "claude-cli/2.2.0 (external, cli)",
		PackageVersion: "0.95.0",
		RuntimeVersion: "v26.4.0",
		OS:             "MacOS",
		Arch:           "arm64",
	}}
	incoming := claudeDeviceHeaders("claude-cli/2.2.0 (external, cli)")
	incoming.Set("X-Stainless-Package-Version", "0.95.0")
	incoming.Set("X-Stainless-Runtime-Version", "v26.4.0")

	ApplyClaudeLegacyDeviceHeaders(request, incoming, cfg, true)

	if got := request.Header.Get("User-Agent"); got != "claude-cli/2.2.0 (external, cli)" {
		t.Fatalf("User-Agent = %q, want configured measured baseline", got)
	}
	if got := request.Header.Get("X-Stainless-Package-Version"); got != "0.95.0" {
		t.Fatalf("X-Stainless-Package-Version = %q, want 0.95.0", got)
	}
	if got := request.Header.Get("X-Stainless-Runtime-Version"); got != "v26.4.0" {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want v26.4.0", got)
	}
}

func TestResolveClaudeDeviceProfilePreservesConfirmedClientAtBaselineVersion(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-baseline-entrypoint"}
	headers := claudeDeviceHeaders("claude-cli/2.1.280 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "0.112.1")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")

	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", headers, nil)
	if errProfile != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() error = %v", errProfile)
	}
	if profile.UserAgent != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("UserAgent = %q, want confirmed cli entrypoint preserved", profile.UserAgent)
	}
	if profile.PackageVersion != "0.112.1" || profile.RuntimeVersion != "v26.3.0" {
		t.Fatalf("software profile = %s/%s, want 0.112.1/v26.3.0", profile.PackageVersion, profile.RuntimeVersion)
	}
}

func TestResolveClaudeDeviceProfileSeparatesVSCodeAgentSDKFromCLI(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-subclient-isolation"}

	cliHeaders := claudeDeviceHeaders("claude-cli/2.1.280 (external, cli)")
	cliHeaders.Set("X-Stainless-Package-Version", "0.112.1")
	cliHeaders.Set("X-Stainless-Runtime-Version", "v26.3.0")
	cliProfile, errCLI := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", cliHeaders, nil)
	if errCLI != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() CLI error = %v", errCLI)
	}

	vscodeUA := "claude-cli/2.1.280 (external, claude-vscode, agent-sdk/0.3.220)"
	vscodeHeaders := claudeDeviceHeaders(vscodeUA)
	vscodeHeaders.Set("X-Stainless-Package-Version", "0.112.1")
	vscodeHeaders.Set("X-Stainless-Runtime-Version", "v26.3.0")
	vscodeProfile, errVSCode := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", vscodeHeaders, nil)
	if errVSCode != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() VSCode error = %v", errVSCode)
	}

	if cliProfile.UserAgent != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("CLI UserAgent = %q, want CLI profile", cliProfile.UserAgent)
	}
	if vscodeProfile.UserAgent != vscodeUA {
		t.Fatalf("VSCode UserAgent = %q, want %q", vscodeProfile.UserAgent, vscodeUA)
	}

	cliProfileAgain, errCLIAgain := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", cliHeaders, nil)
	if errCLIAgain != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() second CLI error = %v", errCLIAgain)
	}
	if cliProfileAgain.UserAgent != cliProfile.UserAgent {
		t.Fatalf("second CLI UserAgent = %q, want isolated cached %q", cliProfileAgain.UserAgent, cliProfile.UserAgent)
	}
}

func TestResolveClaudeDeviceProfileRequiredKeepsLocalCache(t *testing.T) {
	ResetClaudeDeviceProfileCache()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	cfg := &config.Config{}

	first, errFirst := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", claudeDeviceHeaders(defaultClaudeFingerprintUserAgent), cfg)
	if errFirst != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() first error = %v", errFirst)
	}
	second, errSecond := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "api-key", nil, cfg)
	if errSecond != nil {
		t.Fatalf("ResolveClaudeDeviceProfileRequired() second error = %v", errSecond)
	}
	if second.UserAgent != first.UserAgent {
		t.Fatalf("cached UserAgent = %q, want %q", second.UserAgent, first.UserAgent)
	}
}
