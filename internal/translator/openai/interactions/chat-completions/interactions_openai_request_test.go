package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"今天北京的天气怎么样？"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}],"tool_choice":"auto","max_completion_tokens":128}`)
	out, _ := ConvertOpenAIRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "system_instruction").String(); got != "be brief" {
		t.Fatalf("system_instruction = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "get_weather" {
		t.Fatalf("tool name = %q, want get_weather. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema missing. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 128 {
		t.Fatalf("max_output_tokens = %d, want 128. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIRequestToInteractionsMapsToolCallsAndResults(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	out, _ := ConvertOpenAIRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.id").String(); got != "call_1" {
		t.Fatalf("id = %q, want call_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.call_id").Exists() {
		t.Fatalf("function_call should not have call_id parameter. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.0.arguments.q").String(); got != "x" {
		t.Fatalf("arguments.q = %q, want x. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "lookup" {
		t.Fatalf("name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "call_1" {
		t.Fatalf("call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.1.id").Exists() {
		t.Fatalf("function_result should not have id parameter. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.result").String(); got != "ok" {
		t.Fatalf("result = %q, want ok. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIRequestToInteractionsInfersToolNamesForOutOfOrderResults(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"q\":\"x\"}"}},
					{"id": "call_2", "type": "function", "function": {"name": "weather", "arguments": "{\"city\":\"bj\"}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_2", "content": "sunny"},
			{"role": "tool", "tool_call_id": "call_1", "content": "found"}
		]
	}`)
	out, _ := ConvertOpenAIRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	// input.0: function_call call_1 (lookup)
	// input.1: function_call call_2 (weather)
	// input.2: function_result call_2 (weather)
	// input.3: function_result call_1 (lookup)
	if got := gjson.GetBytes(out, "input.2.call_id").String(); got != "call_2" {
		t.Fatalf("input.2.call_id = %q, want call_2. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.name").String(); got != "weather" {
		t.Fatalf("input.2.name = %q, want weather. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.3.call_id").String(); got != "call_1" {
		t.Fatalf("input.3.call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.3.name").String(); got != "lookup" {
		t.Fatalf("input.3.name = %q, want lookup. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIRequestToInteractions_PreservesEnvironmentIDAndPreviousInteractionID(t *testing.T) {
	raw := []byte(`{
		"model":"acme-preview-05-2026",
		"messages":[{"role":"user","content":"continue"}],
		"previous_response_id":"v1_prev123",
		"environment_id":"env_456"
	}`)
	out, _ := ConvertOpenAIRequestToInteractions("acme-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "previous_interaction_id").String(); got != "v1_prev123" {
		t.Fatalf("previous_interaction_id = %q, want v1_prev123. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_456" {
		t.Fatalf("environment_id = %q, want env_456. Output: %s", got, string(out))
	}
}
