package auth

import (
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// removedAuthProviders lists credential types whose providers are no longer
// supported. Leftover credential files of these types are ignored.
var removedAuthProviders = map[string]struct{}{
	"gemini":      {},
	"gemini-cli":  {},
	"antigravity": {},
	"vertex":      {},
	"aistudio":    {},
	"kimi":        {},
	"kimi-ai":     {},
	"kimi.ai":     {},
	"kimi.com":    {},
	"devin":       {},
}

var removedAuthProviderWarned sync.Map

// IsRemovedAuthProvider reports whether provider names a credential type that
// is no longer supported.
func IsRemovedAuthProvider(provider string) bool {
	_, ok := removedAuthProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

// SkipRemovedAuthProvider reports whether a credential file must be ignored
// because its provider is no longer supported, logging a warning once per file.
func SkipRemovedAuthProvider(path, provider string) bool {
	if !IsRemovedAuthProvider(provider) {
		return false
	}
	if _, loaded := removedAuthProviderWarned.LoadOrStore(path, struct{}{}); !loaded {
		log.WithFields(log.Fields{"path": path, "type": provider}).Warn("ignoring credential file for a removed provider")
	}
	return true
}
