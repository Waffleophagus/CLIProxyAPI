// Package redact scrubs credentials from free-form text, such as upstream
// error messages, before it is logged or persisted. It is shared by the log
// diagnostics and the usage store so both apply the same rules.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Marker replaces every redacted value.
const Marker = "[redacted]"

// MinSecretLength is the shortest known secret replaced verbatim; shorter
// values would match ordinary text and are not meaningful secrets anyway.
const MinSecretLength = 4

var (
	urlUserinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@"']+@`)
	authSchemePattern  = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	assignmentPattern  = regexp.MustCompile(`(?i)(["']?\b(?:access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|api[_-]?key|x-api-key|client[_-]?secret|private[_-]?key|authorization|password|passwd|secret|token|key)["']?\s*[:=]\s*["']?)[^\s"'&,;{}\[\]]+`)
	// naturalPattern covers prose such as "Incorrect API key provided: xyz".
	naturalPattern = regexp.MustCompile(`(?i)\b((?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|token|secret|password|credentials?)\s+(?:provided|supplied|used|given|received)\s*:?\s*["']?)[^\s"',;\[\]]+`)
	jwtPattern     = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?`)
	// prefixPattern covers vendor key formats (sk-, sk-ant-, sk-proj-, xai-,
	// GitHub/GitLab tokens, Google API keys).
	prefixPattern = regexp.MustCompile(`\b(?:(?:sk|pk|rk|ak|xai|key|tok)[-_][A-Za-z0-9._-]{6,}|(?:ghp|gho|ghu|ghs|ghr|github_pat|glpat)_[A-Za-z0-9_-]{6,}|AIza[A-Za-z0-9_-]{20,})`)
	// longTokenPattern finds long opaque runs; only high-entropy ones are redacted.
	longTokenPattern = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// String removes the given exact secret values and then any credential-shaped
// substrings from text.
func String(text string, secrets ...string) string {
	return Tokens(Secrets(text, secrets...))
}

// Secrets replaces every occurrence of each known secret value. Longer
// secrets are replaced first so a secret containing another stays hidden.
func Secrets(text string, secrets ...string) string {
	if text == "" || len(secrets) == 0 {
		return text
	}
	list := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); len(secret) >= MinSecretLength {
			list = append(list, secret)
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, secret := range list {
		text = strings.ReplaceAll(text, secret, Marker)
	}
	return text
}

// Tokens replaces credential-shaped substrings: URL userinfo, bearer/basic
// values, key=value assignments, "key provided: X" prose, JWTs, vendor key
// prefixes and long high-entropy tokens.
func Tokens(text string) string {
	if text == "" {
		return text
	}
	text = urlUserinfoPattern.ReplaceAllString(text, "${1}"+Marker+"@")
	text = authSchemePattern.ReplaceAllString(text, "$1 "+Marker)
	text = assignmentPattern.ReplaceAllString(text, "${1}"+Marker)
	text = naturalPattern.ReplaceAllString(text, "${1}"+Marker)
	text = jwtPattern.ReplaceAllString(text, Marker)
	text = prefixPattern.ReplaceAllString(text, Marker)
	text = longTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		if highEntropy(token) {
			return Marker
		}
		return token
	})
	return text
}

// highEntropy reports whether a long run mixes letters and digits and is not a
// UUID (request IDs are useful diagnostics and not secrets).
func highEntropy(token string) bool {
	if uuidPattern.MatchString(token) {
		return false
	}
	var letter, digit bool
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}
