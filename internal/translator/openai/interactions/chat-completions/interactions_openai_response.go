package chat_completions

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func interactionsTextStep(stepType, text string) []byte {
	step := []byte(`{"type":"","content":[{"type":"text","text":""}]}`)
	step, _ = sjson.SetBytes(step, "type", stepType)
	step, _ = sjson.SetBytes(step, "content.0.text", text)
	return step
}

func openAIToolCallToInteractionsStep(toolCall gjson.Result) ([]byte, bool) {
	if toolType := toolCall.Get("type").String(); toolType != "" && toolType != "function" {
		return nil, false
	}
	function := toolCall.Get("function")
	if !function.Exists() {
		return nil, false
	}
	step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
	if id := toolCall.Get("id").String(); id != "" {
		step, _ = sjson.SetBytes(step, "id", id)
	}
	name := function.Get("name").String()
	step, _ = sjson.SetBytes(step, "name", name)
	setRawJSONValue(&step, "arguments", function.Get("arguments"), []byte(`{}`))
	return step, true
}

func openAIReasoningTexts(reasoning gjson.Result) []string {
	if reasoning.Type == gjson.String {
		if reasoning.String() == "" {
			return nil
		}
		return []string{reasoning.String()}
	}
	texts := make([]string, 0)
	if reasoning.IsArray() {
		reasoning.ForEach(func(_, item gjson.Result) bool {
			if text := firstNonEmpty(item.Get("text").String(), item.Get("content").String()); text != "" {
				texts = append(texts, text)
			}
			return true
		})
	}
	return texts
}

func setRawJSONValue(out *[]byte, path string, value gjson.Result, fallback []byte) {
	if !value.Exists() {
		*out, _ = sjson.SetRawBytes(*out, path, fallback)
		return
	}
	raw := strings.TrimSpace(value.String())
	if value.Type == gjson.String && gjson.Valid(raw) {
		*out, _ = sjson.SetRawBytes(*out, path, []byte(raw))
		return
	}
	if value.Type == gjson.String {
		*out, _ = sjson.SetBytes(*out, path, value.String())
		return
	}
	*out, _ = sjson.SetRawBytes(*out, path, []byte(value.Raw))
}
