package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDiffOAuthModelAliasChanges_IncludesDisplayName(t *testing.T) {
	oldMap := map[string][]config.OAuthModelAlias{
		"acme": {
			{Name: "claude-opus-4-6-thinking", Alias: "claude-acme-opus-4-6-thinking", DisplayName: "Acme Opus 4.6"},
		},
	}
	newMap := map[string][]config.OAuthModelAlias{
		"acme": {
			{Name: "claude-opus-4-6-thinking", Alias: "claude-acme-opus-4-6-thinking", DisplayName: "Acme Opus 4.6 (Thinking)"},
		},
	}

	changes, affected := DiffOAuthModelAliasChanges(oldMap, newMap)
	expectContains(t, changes, "oauth-model-alias[acme]: updated (1 -> 1 entries)")
	if len(affected) != 1 || affected[0] != "acme" {
		t.Fatalf("expected acme to be affected, got %#v", affected)
	}
}
