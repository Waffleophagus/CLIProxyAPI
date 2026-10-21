package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestNewConfigSynthesizer(t *testing.T) {
	synth := NewConfigSynthesizer()
	if synth == nil {
		t.Fatal("expected non-nil synthesizer")
	}
}

func TestConfigSynthesizer_Synthesize_NilContext(t *testing.T) {
	synth := NewConfigSynthesizer()
	auths, err := synth.Synthesize(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestConfigSynthesizer_Synthesize_NilConfig(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      nil,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestConfigSynthesizer_ClaudeKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{
					APIKey:                  "sk-ant-api-xxx",
					Prefix:                  "main",
					BaseURL:                 "https://api.anthropic.com",
					DisableCooling:          boolPointer(true),
					RebuildMidSystemMessage: true,
					NativeToolSearch:        true,
					FingerprintProfile:      "claude-code-cli",
					Models: []config.ClaudeModel{
						{Name: "claude-3-opus"},
						{Name: "claude-3-sonnet"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	if auths[0].Provider != "claude" {
		t.Errorf("expected provider claude, got %s", auths[0].Provider)
	}
	if auths[0].Label != "claude-apikey" {
		t.Errorf("expected label claude-apikey, got %s", auths[0].Label)
	}
	if auths[0].Prefix != "main" {
		t.Errorf("expected prefix main, got %s", auths[0].Prefix)
	}
	if auths[0].Attributes["api_key"] != "sk-ant-api-xxx" {
		t.Errorf("expected api_key sk-ant-api-xxx, got %s", auths[0].Attributes["api_key"])
	}
	if auths[0].Attributes["config_index"] != "0" {
		t.Errorf("expected config_index 0, got %s", auths[0].Attributes["config_index"])
	}
	if _, ok := auths[0].Attributes["models_hash"]; !ok {
		t.Error("expected models_hash in attributes")
	}
	if got := auths[0].Attributes["rebuild_mid_system_message"]; got != "true" {
		t.Errorf("expected rebuild_mid_system_message=true, got %s", got)
	}
	if got := auths[0].Attributes["fingerprint_profile"]; got != "claude-code-cli" {
		t.Errorf("expected fingerprint_profile=claude-code-cli, got %s", got)
	}
	if got := auths[0].Attributes["native_tool_search"]; got != "true" {
		t.Errorf("expected native_tool_search=true, got %s", got)
	}
	if v, ok := auths[0].Metadata["disable_cooling"].(bool); !ok || !v {
		t.Errorf("expected disable_cooling=true, got %v", auths[0].Metadata["disable_cooling"])
	}
}

func TestConfigSynthesizer_ClaudeKeys_NativeToolSearchDefaultOff(t *testing.T) {
	auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config:      &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "key", BaseURL: "https://gateway.example.com"}}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil || len(auths) != 1 {
		t.Fatalf("synthesize: auths=%d err=%v", len(auths), err)
	}
	if value, exists := auths[0].Attributes["native_tool_search"]; exists {
		t.Fatalf("native_tool_search = %q, want absent by default", value)
	}
}

func TestConfigSynthesizer_ClaudeKeys_SkipsEmptyAndHeaders(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: ""},    // empty, should be skipped
				{APIKey: "   "}, // whitespace, should be skipped
				{APIKey: "valid-key", Headers: map[string]string{"X-Custom": "value"}},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth (empty keys skipped), got %d", len(auths))
	}
	if auths[0].Attributes["header:X-Custom"] != "value" {
		t.Errorf("expected header:X-Custom=value, got %s", auths[0].Attributes["header:X-Custom"])
	}
}

func TestConfigSynthesizer_CodexKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			CodexKey: []config.CodexKey{
				{
					APIKey:               "codex-key-123",
					Prefix:               "dev",
					BaseURL:              "https://api.openai.com",
					ProxyURL:             "http://proxy.local",
					Websockets:           true,
					AlphaSearch:          true,
					DisableCooling:       boolPointer(true),
					DisableCodexCloaking: boolPointer(true),
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	if auths[0].Provider != "codex" {
		t.Errorf("expected provider codex, got %s", auths[0].Provider)
	}
	if auths[0].Label != "codex-apikey" {
		t.Errorf("expected label codex-apikey, got %s", auths[0].Label)
	}
	if auths[0].ProxyURL != "http://proxy.local" {
		t.Errorf("expected proxy_url http://proxy.local, got %s", auths[0].ProxyURL)
	}
	if auths[0].Attributes["websockets"] != "true" {
		t.Errorf("expected websockets=true, got %s", auths[0].Attributes["websockets"])
	}
	if auths[0].Attributes[coreauth.AttributeCodexAlphaSearch] != "true" {
		t.Errorf("expected codex_alpha_search=true, got %s", auths[0].Attributes[coreauth.AttributeCodexAlphaSearch])
	}
	if auths[0].Attributes[coreauth.AttributeCodexDisableCloaking] != "true" {
		t.Errorf("expected codex_disable_cloaking=true, got %s", auths[0].Attributes[coreauth.AttributeCodexDisableCloaking])
	}
	if v, ok := auths[0].Metadata["disable_cooling"].(bool); !ok || !v {
		t.Errorf("expected disable_cooling=true, got %v", auths[0].Metadata["disable_cooling"])
	}
}

func TestConfigSynthesizer_XAIKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			XAIKey: []config.XAIKey{{
				APIKey:         "xai-key-123",
				Prefix:         "grok",
				BaseURL:        "https://api.x.ai/v1",
				ProxyURL:       "http://proxy.local",
				Websockets:     true,
				AlphaSearch:    true,
				DisableCooling: boolPointer(true),
				Headers:        map[string]string{"X-Custom": "value"},
				Models:         []config.XAIModel{{Name: "grok-4.5", Alias: "grok-latest"}},
			}},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "xai" {
		t.Fatalf("provider = %q, want xai", auth.Provider)
	}
	if auth.Label != "xai-apikey" {
		t.Fatalf("label = %q, want xai-apikey", auth.Label)
	}
	if auth.Attributes["websockets"] != "true" {
		t.Fatalf("websockets = %q, want true", auth.Attributes["websockets"])
	}
	if _, exists := auth.Attributes[coreauth.AttributeCodexAlphaSearch]; exists {
		t.Fatal("xAI auth unexpectedly contains codex_alpha_search")
	}
	if auth.Attributes["base_url"] != "https://api.x.ai/v1" {
		t.Fatalf("base_url = %q, want https://api.x.ai/v1", auth.Attributes["base_url"])
	}
	if auth.Attributes["header:X-Custom"] != "value" {
		t.Fatalf("custom header = %q, want value", auth.Attributes["header:X-Custom"])
	}
	if auth.Attributes["models_hash"] == "" {
		t.Fatal("models_hash is empty")
	}
	if auth.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy URL = %q, want http://proxy.local", auth.ProxyURL)
	}
	if disabled, ok := auth.Metadata["disable_cooling"].(bool); !ok || !disabled {
		t.Fatalf("disable_cooling = %#v, want true", auth.Metadata["disable_cooling"])
	}
}

func TestConfigSynthesizer_MetaKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	disableCooling := true
	ctx := &SynthesisContext{
		Config: &config.Config{
			MetaKey: []config.MetaKey{
				{
					APIKey:         "meta-secret",
					BaseURL:        "https://api.meta.ai/v1",
					ProxyURL:       "http://proxy.local",
					DisableCooling: &disableCooling,
					Models: []config.CodexModel{
						{Name: "muse-spark-1.3", Alias: "muse-spark-1.3"},
					},
					Headers: map[string]string{"X-Custom": "value"},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "meta" {
		t.Fatalf("provider = %q, want meta", auth.Provider)
	}
	if auth.Label != "meta-apikey" {
		t.Fatalf("label = %q, want meta-apikey", auth.Label)
	}
	if auth.Attributes["base_url"] != "https://api.meta.ai/v1" {
		t.Fatalf("base_url = %q, want https://api.meta.ai/v1", auth.Attributes["base_url"])
	}
	if auth.Attributes["header:X-Custom"] != "value" {
		t.Fatalf("custom header = %q, want value", auth.Attributes["header:X-Custom"])
	}
	if auth.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy URL = %q, want http://proxy.local", auth.ProxyURL)
	}
}

func TestConfigSynthesizer_XAIKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			XAIKey: []config.CodexKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-xai.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-xai-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-xai.example.com" {
		t.Fatalf("expected base_url=https://custom-xai.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_ClaudeKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-claude.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-claude-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-claude.example.com" {
		t.Fatalf("expected base_url=https://custom-claude.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_CodexKeys_SkipsEmptyAndHeaders(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: ""},   // empty key without base URL, should be skipped
				{APIKey: "  "}, // whitespace key without base URL, should be skipped
				{APIKey: "valid-key", Headers: map[string]string{"Authorization": "Bearer xyz"}},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth (empty keys skipped), got %d", len(auths))
	}
	if auths[0].Attributes["header:Authorization"] != "Bearer xyz" {
		t.Errorf("expected header:Authorization=Bearer xyz, got %s", auths[0].Attributes["header:Authorization"])
	}
	if _, exists := auths[0].Attributes[coreauth.AttributeCodexAlphaSearch]; exists {
		t.Fatal("default alpha-search=false unexpectedly generated codex_alpha_search")
	}
}

func TestConfigSynthesizer_CodexKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			CodexKey: []config.CodexKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-codex.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-codex-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-codex.example.com" {
		t.Fatalf("expected base_url=https://custom-codex.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_OpenAICompat(t *testing.T) {
	tests := []struct {
		name    string
		compat  []config.OpenAICompatibility
		wantLen int
	}{
		{
			name: "with APIKeyEntries",
			compat: []config.OpenAICompatibility{
				{
					Name:           "CustomProvider",
					BaseURL:        "https://custom.api.com",
					DisableCooling: boolPointer(true),
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-1"},
						{APIKey: "key-2"},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "empty APIKeyEntries included (legacy)",
			compat: []config.OpenAICompatibility{
				{
					Name:    "EmptyKeys",
					BaseURL: "https://empty.api.com",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: ""},
						{APIKey: "   "},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "without APIKeyEntries (fallback)",
			compat: []config.OpenAICompatibility{
				{
					Name:    "NoKeyProvider",
					BaseURL: "https://no-key.api.com",
				},
			},
			wantLen: 1,
		},
		{
			name: "empty name defaults",
			compat: []config.OpenAICompatibility{
				{
					Name:    "",
					BaseURL: "https://default.api.com",
				},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := NewConfigSynthesizer()
			ctx := &SynthesisContext{
				Config: &config.Config{
					OpenAICompatibility: tt.compat,
				},
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, err := synth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(auths) != tt.wantLen {
				t.Fatalf("expected %d auths, got %d", tt.wantLen, len(auths))
			}
			if tt.name == "with APIKeyEntries" {
				for i := range auths {
					if v, ok := auths[i].Metadata["disable_cooling"].(bool); !ok || !v {
						t.Fatalf("expected auth[%d].disable_cooling=true, got %v", i, auths[i].Metadata["disable_cooling"])
					}
				}
			}
		})
	}
}

func TestConfigSynthesizer_OpenAICompat_UsesNamespacedProviderKey(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "kimi",
					BaseURL: "https://kimi-compatible.example.com/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "test-key"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "openai-compatible-kimi" {
		t.Fatalf("provider = %q, want openai-compatible-kimi", auth.Provider)
	}
	if auth.Attributes["provider_key"] != "openai-compatible-kimi" {
		t.Fatalf("provider_key = %q, want openai-compatible-kimi", auth.Attributes["provider_key"])
	}
	if auth.Attributes["compat_name"] != "kimi" {
		t.Fatalf("compat_name = %q, want kimi", auth.Attributes["compat_name"])
	}
	if auth.Attributes["config_index"] != "0" {
		t.Fatalf("config_index = %q, want 0", auth.Attributes["config_index"])
	}
}

func TestConfigSynthesizer_OpenAICompat_WithModelsHash(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "TestProvider",
					BaseURL: "https://test.api.com",
					Models: []config.OpenAICompatibilityModel{
						{Name: "model-a"},
						{Name: "model-b"},
					},
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-with-models"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if _, ok := auths[0].Attributes["models_hash"]; !ok {
		t.Error("expected models_hash in attributes")
	}
	if auths[0].Attributes["api_key"] != "key-with-models" {
		t.Errorf("expected api_key key-with-models, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_OpenAICompat_FallbackWithModels(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "NoKeyWithModels",
					BaseURL: "https://nokey.api.com",
					Models: []config.OpenAICompatibilityModel{
						{Name: "model-x"},
					},
					Headers: map[string]string{"X-API": "header-value"},
					// No APIKeyEntries - should use fallback path
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if _, ok := auths[0].Attributes["models_hash"]; !ok {
		t.Error("expected models_hash in fallback path")
	}
	if auths[0].Attributes["header:X-API"] != "header-value" {
		t.Errorf("expected header:X-API=header-value, got %s", auths[0].Attributes["header:X-API"])
	}
}
