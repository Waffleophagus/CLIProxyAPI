package redact

import (
	"strings"
	"testing"
)

func TestStringRedactsKnownSecretsAndTokenShapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		secrets []string
		leaks   []string
		keep    []string
	}{
		{
			name:    "exact upstream key without a known prefix",
			in:      "Incorrect API key provided: local-gateway-secret.",
			secrets: []string{"local-gateway-secret"},
			leaks:   []string{"local-gateway-secret"},
			keep:    []string{"Incorrect API key provided"},
		},
		{
			name:  "openai project key in prose",
			in:    "Incorrect API key provided: sk-proj-ABCDEFGHIJKLMNOPQRST. You can find your API key at https://platform.openai.com/account/api-keys.",
			leaks: []string{"sk-proj-ABCDEFGHIJKLMNOPQRST", "ABCDEFGHIJKLMNOPQRST"},
			keep:  []string{"https://platform.openai.com/account/api-keys"},
		},
		{
			name:  "anthropic key inside JSON message",
			in:    `{"type":"error","error":{"type":"authentication_error","message":"Invalid key sk-ant-api03-abcDEF123456_xyz"}}`,
			leaks: []string{"sk-ant-api03-abcDEF123456_xyz", "abcDEF123456"},
			keep:  []string{"authentication_error"},
		},
		{
			name:  "xai key",
			in:    "bad credentials xai-Zx81kQp0Lm2Nb3Vc4Xz5",
			leaks: []string{"xai-Zx81kQp0Lm2Nb3Vc4Xz5"},
		},
		{
			name:  "jwt",
			in:    "token rejected: eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl",
			leaks: []string{"eyJhbGciOiJSUzI1NiJ9", "c2lnbmF0dXJl"},
		},
		{
			name:  "bearer and assignments",
			in:    `Authorization: Bearer opaque.value-123 api_key=hunter2hunter2 "access_token":"abc123xyz"`,
			leaks: []string{"opaque.value-123", "hunter2hunter2", "abc123xyz"},
		},
		{
			name:  "url userinfo",
			in:    "dial https://user:password@example.com/v1 failed",
			leaks: []string{"user:password"},
			keep:  []string{"example.com/v1"},
		},
		{
			name:  "long high entropy token",
			in:    "session 9f8A7b6C5d4E3f2A1b0C9d8E7f6A5b4C3d2E1f0a expired",
			leaks: []string{"9f8A7b6C5d4E3f2A1b0C9d8E7f6A5b4C3d2E1f0a"},
		},
		{
			name: "plain diagnostics survive",
			in:   "upstream timeout after 30s for model claude-sonnet-4-5-20250929 request 123e4567-e89b-12d3-a456-426614174000",
			keep: []string{"upstream timeout after 30s", "claude-sonnet-4-5-20250929", "123e4567-e89b-12d3-a456-426614174000"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := String(tc.in, tc.secrets...)
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Fatalf("String() leaks %q: %s", leak, got)
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(got, keep) {
					t.Fatalf("String() dropped %q: %s", keep, got)
				}
			}
		})
	}
}

func TestSecretsReplacesLongestFirstAndIgnoresTinyValues(t *testing.T) {
	got := Secrets("key abcd-efgh and abcd and x", "abcd", "abcd-efgh", "x", "  ")
	if got != "key [redacted] and [redacted] and x" {
		t.Fatalf("Secrets() = %q", got)
	}
}
