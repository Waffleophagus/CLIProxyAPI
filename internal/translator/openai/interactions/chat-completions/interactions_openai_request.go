package chat_completions

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func copyNumber(out *[]byte, path string, value gjson.Result) {
	if value.Exists() {
		*out, _ = sjson.SetRawBytes(*out, path, []byte(value.Raw))
	}
}

func jsonStringValue(value gjson.Result, fallback string) string {
	if !value.Exists() {
		return fallback
	}
	if value.Type == gjson.String {
		return value.String()
	}
	return value.Raw
}

func firstExisting(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
