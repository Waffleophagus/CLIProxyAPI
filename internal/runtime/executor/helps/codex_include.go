package helps

import (
	neturl "net/url"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexOpenAIIncludeEntry struct {
	value        string
	platformOnly bool
}

// codexOpenAIResponsesOptionalIncludes lists the official OpenAI Responses
// ResponseIncludable entries (other than the always-required
// reasoning.encrypted_content) plus Codex's opt-in output.encrypted_content.
// Entries without platformOnly match the relative order emitted by the
// Responses-to-Codex translator so requests without platform-only values are a
// fast no-op.
var codexOpenAIResponsesOptionalIncludes = [...]codexOpenAIIncludeEntry{
	{value: "web_search_call.action.sources"},
	{value: "web_search_call.results"},
	{value: "file_search_call.results", platformOnly: true},
	{value: "message.input_image.image_url", platformOnly: true},
	{value: "computer_call_output.output.image_url", platformOnly: true},
	{value: "code_interpreter_call.outputs", platformOnly: true},
	{value: "message.output_text.logprobs", platformOnly: true},
	{value: "output.encrypted_content"},
}

// IsChatGPTCodexBackendBaseURL reports whether baseURL targets the ChatGPT
// Codex backend (or is empty or unparseable, failing closed to the conservative
// ChatGPT Codex allowlist).
func IsChatGPTCodexBackendBaseURL(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return true
	}
	parsed, errParse := neturl.Parse(trimmed)
	if errParse != nil {
		return true
	}
	if !strings.EqualFold(strings.TrimRight(parsed.Hostname(), "."), "chatgpt.com") {
		return false
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	return cleanPath == "/backend-api/codex" || strings.HasPrefix(cleanPath, "/backend-api/codex/")
}

// PreserveCodexOpenAIResponsesInclude restores explicitly requested official
// OpenAI Responses include values (and output.encrypted_content) from the
// caller's OpenAI Responses payload onto body when targeting a non-ChatGPT
// OpenAI Responses upstream (such as SIWC or a custom codex-api-key endpoint).
// Unknown values and non-string entries remain filtered, and
// reasoning.encrypted_content is always guaranteed first. body is returned
// unchanged when there is nothing to restore.
func PreserveCodexOpenAIResponsesInclude(body []byte, source []byte, from sdktranslator.Format) []byte {
	if len(body) == 0 || len(source) == 0 || !strings.EqualFold(strings.TrimSpace(from.String()), sdktranslator.FormatOpenAIResponse.String()) {
		return body
	}
	current := gjson.GetBytes(source, "include")
	if !current.IsArray() {
		return body
	}

	var present [len(codexOpenAIResponsesOptionalIncludes)]bool
	hasPlatformOnly := false
	for _, value := range current.Array() {
		if value.Type != gjson.String {
			continue
		}
		entry := value.String()
		for i, allowed := range codexOpenAIResponsesOptionalIncludes {
			if entry == allowed.value {
				present[i] = true
				if allowed.platformOnly {
					hasPlatformOnly = true
				}
				break
			}
		}
	}
	if !hasPlatformOnly {
		return body
	}

	encoded := make([]byte, 0, 256)
	encoded = append(encoded, `["reasoning.encrypted_content"`...)
	for i, allowed := range codexOpenAIResponsesOptionalIncludes {
		if !present[i] {
			continue
		}
		encoded = append(encoded, `,"`...)
		encoded = append(encoded, allowed.value...)
		encoded = append(encoded, '"')
	}
	encoded = append(encoded, ']')
	updated, errSet := sjson.SetRawBytes(body, "include", encoded)
	if errSet != nil {
		return body
	}
	return updated
}

// StripResponsesIncludeValues removes any listed string values from body's
// include array while preserving remaining entries and order. It reports
// whether anything was removed.
func StripResponsesIncludeValues(body []byte, blocked ...string) ([]byte, bool) {
	if len(body) == 0 || len(blocked) == 0 {
		return body, false
	}
	current := gjson.GetBytes(body, "include")
	if !current.IsArray() {
		return body, false
	}
	values := current.Array()
	if len(values) == 0 {
		return body, false
	}
	isBlocked := func(s string) bool {
		for _, b := range blocked {
			if s == b {
				return true
			}
		}
		return false
	}

	removed := false
	kept := make([][]byte, 0, len(values))
	for _, value := range values {
		if value.Type == gjson.String && isBlocked(value.String()) {
			removed = true
			continue
		}
		kept = append(kept, []byte(value.Raw))
	}
	if !removed {
		return body, false
	}
	if len(kept) == 0 {
		updated, errDelete := sjson.DeleteBytes(body, "include")
		if errDelete != nil {
			return body, false
		}
		return updated, true
	}
	encoded := make([]byte, 0, len(current.Raw))
	encoded = append(encoded, '[')
	for i, raw := range kept {
		if i > 0 {
			encoded = append(encoded, ',')
		}
		encoded = append(encoded, raw...)
	}
	encoded = append(encoded, ']')
	updated, errSet := sjson.SetRawBytes(body, "include", encoded)
	if errSet != nil {
		return body, false
	}
	return updated, true
}
