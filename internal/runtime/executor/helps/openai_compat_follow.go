package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// OpenAICompatFollowPendingAlias reports the alias of a follow-mode model entry
// whose upstream model has not been resolved yet and that model would route to.
func OpenAICompatFollowPendingAlias(compat *config.OpenAICompatibility, model string) (string, bool) {
	if compat == nil {
		return "", false
	}
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return "", false
	}
	for i := range compat.Models {
		entry := &compat.Models[i]
		if entry.FollowPending && strings.EqualFold(strings.TrimSpace(entry.Name), model) {
			return strings.TrimSpace(entry.Alias), true
		}
	}
	return "", false
}
