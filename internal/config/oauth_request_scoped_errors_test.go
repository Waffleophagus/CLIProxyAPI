package config

import (
	"testing"
)

func TestParseConfigOAuthRequestScopedErrors(t *testing.T) {
	const yamlConfig = `
oauth-request-scoped-errors:
  vendora:
    - status: 400
      match:
        - "maximum_context_length"
        - "context_length_exceeded"
      match-regexr:
        - "maximum_context_length$"
        - "^context_length_exceeded"
      action: "stop"
  acmestudio:
    - status: 400
      match:
        - "invalid_argument"
      action: "continue"
  acme:
    - status: 500
      match:
        - "internal_server_error"
      action: "stop-and-cooldown"
  claude:
    - status: 429
      match:
        - "rate_limit"
      action: "continue-and-cooldown"
  codex:
    - status: 400
      match:
        - "context_window_exceeded"
      action: "stop"
  kimi:
    - status: 400
      match:
        - "length_limit"
      action: "stop"
  xai:
    - status: 400
      match:
        - "max_tokens_exceeded"
      action: "stop"
  meta:
    - status: 400
      match:
        - "context_length_exceeded"
      action: "stop"
`

	cfg, err := ParseConfigBytes([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("ParseConfigFromBytes failed: %v", err)
	}

	if len(cfg.OAuthRequestScopedErrors) != 8 {
		t.Fatalf("cfg.OAuthRequestScopedErrors len = %d, want 8", len(cfg.OAuthRequestScopedErrors))
	}

	vendoraRules, ok := cfg.OAuthRequestScopedErrors["vendora"]
	if !ok || len(vendoraRules) != 1 {
		t.Fatalf("vendora rules missing or len != 1: %#v", vendoraRules)
	}
	rule := vendoraRules[0]
	if rule.Status != 400 || rule.Action != "stop" {
		t.Errorf("unexpected vendora rule: %+v", rule)
	}
	if len(rule.Match) != 2 || len(rule.MatchRegexr) != 2 {
		t.Errorf("unexpected vendora match len: %+v", rule)
	}
}

func TestSanitizeOAuthRequestScopedErrors(t *testing.T) {
	cfg := &Config{
		OAuthRequestScopedErrors: map[string][]RequestScopedErrorRule{
			" Vendora ": {
				{
					Status:      400,
					Match:       []string{"  context_length  ", ""},
					MatchRegexr: []string{"  ^error.*  ", ""},
					Action:      " STOP ",
				},
				{
					Status: 0, // invalid status
					Match:  []string{"foo"},
					Action: "stop",
				},
				{
					Status: 400, // missing match / action
				},
			},
			" empty-channel ": {},
		},
	}

	cfg.SanitizeOAuthRequestScopedErrors()

	if len(cfg.OAuthRequestScopedErrors) != 1 {
		t.Fatalf("expected 1 sanitized channel, got %d", len(cfg.OAuthRequestScopedErrors))
	}

	rules := cfg.OAuthRequestScopedErrors["vendora"]
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule for vendora, got %d", len(rules))
	}
	if rules[0].Status != 400 || rules[0].Action != "stop" {
		t.Errorf("unexpected sanitized rule: %+v", rules[0])
	}
	if len(rules[0].Match) != 1 || rules[0].Match[0] != "context_length" {
		t.Errorf("unexpected sanitized match: %+v", rules[0].Match)
	}
	if len(rules[0].MatchRegexr) != 1 || rules[0].MatchRegexr[0] != "^error.*" {
		t.Errorf("unexpected sanitized regexr: %+v", rules[0].MatchRegexr)
	}
}
