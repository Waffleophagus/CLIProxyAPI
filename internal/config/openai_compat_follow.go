package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultFollowInterval is the poll interval for follow-mode models.
	DefaultFollowInterval = 30 * time.Second
	// MinFollowInterval is the smallest accepted follow-mode poll interval.
	MinFollowInterval = 5 * time.Second
)

// IsFollowing reports whether the entry resolves its upstream model at runtime.
// A pinned name disables polling for the entry.
func (m OpenAICompatibilityModel) IsFollowing() bool {
	return m.Follow && strings.TrimSpace(m.Name) == ""
}

// HasFollowModels reports whether any model entry of the provider needs polling.
func (c *OpenAICompatibility) HasFollowModels() bool {
	if c == nil {
		return false
	}
	for i := range c.Models {
		if c.Models[i].IsFollowing() {
			return true
		}
	}
	return false
}

// FollowPollInterval returns the effective follow-mode poll interval.
func (c *OpenAICompatibility) FollowPollInterval() time.Duration {
	if c == nil {
		return DefaultFollowInterval
	}
	raw := strings.TrimSpace(c.FollowInterval)
	if raw == "" {
		return DefaultFollowInterval
	}
	interval, errParse := time.ParseDuration(raw)
	if errParse != nil || interval <= 0 {
		return DefaultFollowInterval
	}
	if interval < MinFollowInterval {
		return MinFollowInterval
	}
	return interval
}

// HasOpenAICompatibilityFollow reports whether any provider uses follow mode.
func (cfg *Config) HasOpenAICompatibilityFollow() bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.OpenAICompatibility {
		for j := range cfg.OpenAICompatibility[i].Models {
			if cfg.OpenAICompatibility[i].Models[j].Follow {
				return true
			}
		}
	}
	return false
}

// ValidateOpenAICompatibilityFollow rejects incomplete follow-mode settings.
func (cfg *Config) ValidateOpenAICompatibilityFollow() error {
	if cfg == nil {
		return nil
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		label := strings.TrimSpace(compat.Name)
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if raw := strings.TrimSpace(compat.FollowInterval); raw != "" {
			interval, errParse := time.ParseDuration(raw)
			if errParse != nil || interval <= 0 {
				return fmt.Errorf("api-keys.openai-compatibility %s: invalid follow-interval %q", label, raw)
			}
		}
		for j := range compat.Models {
			model := &compat.Models[j]
			if strings.TrimSpace(model.Match) != "" && !model.Follow {
				return fmt.Errorf("api-keys.openai-compatibility %s: models[%d].match requires follow: true", label, j)
			}
			if model.Follow && strings.TrimSpace(model.Alias) == "" {
				return fmt.Errorf("api-keys.openai-compatibility %s: models[%d] with follow: true requires alias", label, j)
			}
		}
	}
	return nil
}
