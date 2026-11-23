package helps

import (
	"slices"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIsChatGPTCodexBackendBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{name: "empty defaults to chatgpt codex", baseURL: "", want: true},
		{name: "whitespace defaults to chatgpt codex", baseURL: "   ", want: true},
		{name: "standard chatgpt codex", baseURL: "https://chatgpt.com/backend-api/codex", want: true},
		{name: "trailing slash chatgpt codex", baseURL: "https://chatgpt.com/backend-api/codex/", want: true},
		{name: "uppercase host and port", baseURL: "https://CHATGPT.COM:443/backend-api/codex", want: true},
		{name: "trailing dns dot host", baseURL: "https://chatgpt.com./backend-api/codex", want: true},
		{name: "uppercase trailing dns dot host and slash", baseURL: "https://CHATGPT.COM./backend-api/codex/", want: true},
		{name: "nested chatgpt codex path", baseURL: "https://chatgpt.com/backend-api/codex/v1", want: true},
		{name: "websocket chatgpt codex", baseURL: "wss://chatgpt.com/backend-api/codex", want: true},
		{name: "malformed url fails closed", baseURL: "://bad-url", want: true},
		{name: "different chatgpt path prefix", baseURL: "https://chatgpt.com/backend-api/codex-other", want: false},
		{name: "openai platform api", baseURL: "https://api.openai.com/v1", want: false},
		{name: "loopback mock", baseURL: "http://127.0.0.1:8318", want: false},
		{name: "custom gateway with codex path", baseURL: "https://proxy.example.com/backend-api/codex", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsChatGPTCodexBackendBaseURL(tt.baseURL); got != tt.want {
				t.Fatalf("IsChatGPTCodexBackendBaseURL(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestPreserveCodexOpenAIResponsesInclude(t *testing.T) {
	t.Run("no-op when no platform-only include requested", func(t *testing.T) {
		doc := []byte(`{"include":["reasoning.encrypted_content","web_search_call.results"]}`)
		source := []byte(`{"include":["web_search_call.results","output.encrypted_content"]}`)
		if got := PreserveCodexOpenAIResponsesInclude(doc, source, sdktranslator.FormatOpenAIResponse); string(got) != string(doc) {
			t.Fatalf("expected no-op, got changed doc: %s", string(got))
		}
	})

	t.Run("no-op for non-responses source format", func(t *testing.T) {
		doc := []byte(`{"include":["reasoning.encrypted_content"]}`)
		source := []byte(`{"include":["file_search_call.results"]}`)
		if got := PreserveCodexOpenAIResponsesInclude(doc, source, sdktranslator.FormatOpenAI); string(got) != string(doc) {
			t.Fatalf("expected no-op for non-responses format, got changed doc: %s", string(got))
		}
	})

	t.Run("restores official includables and filters unknown values", func(t *testing.T) {
		doc := []byte(`{"include":["reasoning.encrypted_content","web_search_call.action.sources","web_search_call.results","output.encrypted_content"]}`)
		source := []byte(`{"include":["message.output_text.logprobs","file_search_call.results","web_search_call.results","web_search_call.action.sources","message.input_image.image_url","computer_call_output.output.image_url","code_interpreter_call.outputs","output.encrypted_content","file_search_call.results","unknown.value",123,null]}`)
		updated := PreserveCodexOpenAIResponsesInclude(doc, source, sdktranslator.FormatOpenAIResponse)
		if string(updated) == string(doc) {
			t.Fatal("expected include update")
		}
		doc = updated
		arr := gjson.GetBytes(doc, "include").Array()
		got := make([]string, 0, len(arr))
		for _, item := range arr {
			got = append(got, item.String())
		}
		want := []string{
			"reasoning.encrypted_content",
			"web_search_call.action.sources",
			"web_search_call.results",
			"file_search_call.results",
			"message.input_image.image_url",
			"computer_call_output.output.image_url",
			"code_interpreter_call.outputs",
			"message.output_text.logprobs",
			"output.encrypted_content",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("include = %v, want %v", got, want)
		}
	})
}

func TestStripResponsesIncludeValues(t *testing.T) {
	t.Run("removes blocked entries and preserves order", func(t *testing.T) {
		doc := []byte(`{"include":["reasoning.encrypted_content","web_search_call.action.sources","web_search_call.results","output.encrypted_content"]}`)
		doc, changed := StripResponsesIncludeValues(doc, "output.encrypted_content", "web_search_call.results")
		if !changed {
			t.Fatal("expected change")
		}
		if got := gjson.GetBytes(doc, "include").Raw; got != `["reasoning.encrypted_content","web_search_call.action.sources"]` {
			t.Fatalf("include = %s", got)
		}
	})

	t.Run("deletes include when all entries removed", func(t *testing.T) {
		doc := []byte(`{"include":["output.encrypted_content"]}`)
		doc, changed := StripResponsesIncludeValues(doc, "output.encrypted_content")
		if !changed {
			t.Fatal("expected change")
		}
		if gjson.GetBytes(doc, "include").Exists() {
			t.Fatalf("expected include deleted, got %s", gjson.GetBytes(doc, "include").Raw)
		}
	})

	t.Run("no-op when blocked entry absent", func(t *testing.T) {
		doc := []byte(`{"include":["reasoning.encrypted_content"]}`)
		if got, changed := StripResponsesIncludeValues(doc, "output.encrypted_content"); changed {
			t.Fatalf("expected no-op, got %s", string(got))
		}
	})
}
