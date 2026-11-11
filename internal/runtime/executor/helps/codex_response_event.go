package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// CodexResponseEventType recognizes error envelopes that omit the event type.
// Explicit event types remain authoritative so output and metadata cannot be
// mistaken for a terminal failure merely because they contain an error field.
func CodexResponseEventType(payload []byte) string {
	if eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String()); eventType != "" {
		return eventType
	}
	if !gjson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{"error", "body.error", "response.error"} {
		node := gjson.GetBytes(payload, path)
		if node.IsObject() || node.Type == gjson.String && strings.TrimSpace(node.String()) != "" {
			return "error"
		}
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "code").String()) != "" &&
		strings.TrimSpace(gjson.GetBytes(payload, "message").String()) != "" {
		return "error"
	}
	return ""
}
