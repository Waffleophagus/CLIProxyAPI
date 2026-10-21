package auth

import "strings"

// secretFieldNames are attribute/metadata keys (lowercased, without "_" or
// "-") whose string values are credentials.
var secretFieldNames = map[string]bool{
	"apikey":       true,
	"accesstoken":  true,
	"refreshtoken": true,
	"idtoken":      true,
	"sessiontoken": true,
	"setuptoken":   true,
	"dcatoken":     true,
	"token":        true,
	"clientsecret": true,
	"secret":       true,
	"password":     true,
	"cookie":       true,
}

// CredentialSecrets returns the raw credential values held by auth (API keys,
// OAuth tokens, secret custom headers) so callers can redact them from
// upstream text before logging or persisting it. The result must never be
// logged itself.
func CredentialSecrets(auth *Auth) []string {
	if auth == nil {
		return nil
	}
	var out []string
	add := func(value string) {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	for key, value := range auth.Attributes {
		if name, ok := strings.CutPrefix(key, "header:"); ok {
			if sensitiveHeaderName(name) {
				add(value)
				// "Bearer xyz": also hide the bare credential.
				if fields := strings.Fields(value); len(fields) > 1 {
					add(fields[len(fields)-1])
				}
			}
			continue
		}
		if isSecretFieldName(key) {
			add(value)
		}
	}
	collectSecretValues(auth.Metadata, 0, add)
	return out
}

func collectSecretValues(value any, depth int, add func(string)) {
	if depth > 3 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if text, ok := nested.(string); ok {
				if isSecretFieldName(key) {
					add(text)
				}
				continue
			}
			collectSecretValues(nested, depth+1, add)
		}
	case map[string]string:
		for key, text := range typed {
			if isSecretFieldName(key) {
				add(text)
			}
		}
	}
}

func isSecretFieldName(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(key)))
	return secretFieldNames[key]
}

func sensitiveHeaderName(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range []string{"authorization", "key", "token", "secret", "cookie", "password"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
