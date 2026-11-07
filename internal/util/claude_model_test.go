package util

import "testing"

func TestIsClaudeLegacySystemReminderModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{"listed model", "claude-sonnet-4-5", true},
		{"listed snapshot", "claude-haiku-4-5-20251001", true},
		{"mixed case and whitespace", "  Claude-Opus-4-6 ", true},
		{"provider prefix", "anthropic/claude-sonnet-4-6", true},
		{"supported model", "claude-opus-5-5", false},
		{"unknown future model", "claude-sonnet-9", false},
		{"unlisted suffix", "claude-sonnet-4-5-thinking", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeLegacySystemReminderModel(tt.model); got != tt.expected {
				t.Errorf("IsClaudeLegacySystemReminderModel(%q) = %v, expected %v", tt.model, got, tt.expected)
			}
		})
	}
}
