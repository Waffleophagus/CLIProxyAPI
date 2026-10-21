package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWarnLogUpstreamFailureRedactsEchoedCredentials(t *testing.T) {
	cases := []struct {
		name    string
		auth    *Auth
		message string
		secrets []string
	}{
		{
			name:    "exact upstream api key",
			auth:    &Auth{ID: "auth-1", Provider: "openai-compatibility", Attributes: map[string]string{"api_key": "local-gateway-secret"}},
			message: "Incorrect API key provided: local-gateway-secret",
			secrets: []string{"local-gateway-secret"},
		},
		{
			name:    "openai project key in prose",
			auth:    &Auth{ID: "auth-2", Provider: "openai"},
			message: "Incorrect API key provided: sk-proj-ABCDEFGHIJKLMNOPQRST",
			secrets: []string{"sk-proj-ABCDEFGHIJKLMNOPQRST", "ABCDEFGHIJKLMNOPQRST"},
		},
		{
			name:    "anthropic key in JSON message",
			auth:    &Auth{ID: "auth-3", Provider: "claude"},
			message: `{"type":"error","error":{"message":"Invalid key sk-ant-api03-abcDEF123456_xyz"}}`,
			secrets: []string{"sk-ant-api03-abcDEF123456_xyz", "abcDEF123456"},
		},
		{
			name:    "exact oauth access token",
			auth:    &Auth{ID: "auth-4", Provider: "codex", Metadata: map[string]any{"access_token": "opaque.oauth.value"}},
			message: "the bearer opaque.oauth.value was revoked; token opaque.oauth.value",
			secrets: []string{"opaque.oauth.value"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := setupTestLoggerHook(t)
			warnLogUpstreamFailure(context.Background(), nil, tc.auth.Provider, "m", tc.auth, time.Millisecond,
				statusErrorLogTestError{message: tc.message, statusCode: http.StatusUnauthorized})
			found := false
			for _, entry := range hook.AllEntries() {
				if !strings.Contains(entry.Message, "upstream execution failed") {
					continue
				}
				found = true
				for _, secret := range tc.secrets {
					if strings.Contains(entry.Message, secret) {
						t.Fatalf("Warn log leaks %q: %s", secret, entry.Message)
					}
				}
			}
			if !found {
				t.Fatal("expected upstream failure Warn log")
			}
		})
	}
}

func TestCredentialSecretsCollectsKeysTokensAndSecretHeaders(t *testing.T) {
	auth := &Auth{
		Attributes: map[string]string{
			"api_key":              "attr-key",
			"base_url":             "https://example.com",
			"provider_key":         "openai-compatible-x",
			"header:Authorization": "Bearer header-token",
			"header:X-Trace":       "not-secret",
		},
		Metadata: map[string]any{
			"access_token":  "meta-access",
			"refresh_token": "meta-refresh",
			"email":         "user@example.com",
			"token":         map[string]any{"id_token": "nested-id"},
		},
	}
	got := strings.Join(CredentialSecrets(auth), "\n")
	for _, want := range []string{"attr-key", "Bearer header-token", "header-token", "meta-access", "meta-refresh", "nested-id"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CredentialSecrets missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"https://example.com", "openai-compatible-x", "not-secret", "user@example.com"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("CredentialSecrets included %q", unwanted)
		}
	}
}
