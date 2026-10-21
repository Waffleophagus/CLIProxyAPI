package signature

import (
	"strings"

	"github.com/tidwall/gjson"
)

func isEmptyClaudeThinkingPlaceholder(part gjson.Result) bool {
	if strings.TrimSpace(part.Get("signature").String()) != "" {
		return false
	}
	return strings.TrimSpace(claudeThinkingBlockText(part)) == ""
}

func claudeThinkingBlockText(part gjson.Result) string {
	if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}

	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}
	if thinkingField.Type == gjson.String {
		return thinkingField.String()
	}
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
	}
	return ""
}
