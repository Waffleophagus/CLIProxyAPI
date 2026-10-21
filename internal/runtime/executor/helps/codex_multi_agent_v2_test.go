package helps

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSameByteSlice(t *testing.T) {
	buf := []byte("payload")
	cases := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"identical slice", buf, buf, true},
		{"same array same length", buf[:3], buf[:3], true},
		{"equal bytes different array", buf, append([]byte(nil), buf...), false},
		{"different length", buf, buf[:3], false},
		{"both nil", nil, nil, true},
		{"nil and empty", nil, []byte{}, true},
		{"nil and non-empty", nil, buf, false},
		{"offset alias", buf, buf[1:], false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameByteSlice(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameByteSlice() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTranslateRequestCompatibilityForExecutorToolIntegerTypes(t *testing.T) {
	const responsesPayload = `{"input":"hi","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"},"unrelated":{"type":"number"}}}}]}`
	const claudePayload = `{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"exec_command","input_schema":{"type":"object","properties":{"yield_time_ms":{"type":"number"},"unrelated":{"type":"number"}}}}]}`
	for _, route := range []struct {
		name       string
		from, to   sdktranslator.Format
		payload    string
		properties string
	}{
		{name: "responses_to_codex", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatCodex, payload: responsesPayload, properties: "tools.0.parameters.properties"},
		{name: "claude_to_codex", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, payload: claudePayload, properties: "tools.0.parameters.properties"},
		{name: "responses_to_claude", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, payload: responsesPayload, properties: "tools.0.input_schema.properties"},
		{name: "responses_passthrough", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAIResponse, payload: responsesPayload, properties: "tools.0.parameters.properties"},
	} {
		for _, target := range []struct {
			name     string
			preserve bool
		}{
			{name: "codex", preserve: true},
			{name: "codex-websockets", preserve: true},
			{name: "xai"},
			{name: "meta"},
			{name: ""},
		} {
			for _, ua := range []string{"codex_cli_rs/0.1", "curl/8.7.1", ""} {
				for _, compat := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/target=%s/ua=%s/compat=%t", route.name, target.name, ua, compat), func(t *testing.T) {
						var headers http.Header
						if ua != "" {
							headers = http.Header{"User-Agent": []string{ua}, "X-Openai-Subagent": []string{"collab_spawn"}}
						}
						payload := []byte(route.payload)
						out, _ := TranslateRequestWithAPIKeyModelCompatibilityCheckedForExecutor(t.Context(), headers, &config.Config{}, target.name, route.from, route.to, "model", payload, false, compat)
						outputs := map[string][]byte{
							"update_intent": out,
							"body":          TranslateRequestWithAPIKeyModelCompatibilityForExecutor(t.Context(), headers, &config.Config{}, target.name, route.from, route.to, "model", payload, false, compat),
						}
						if target.name == "" {
							outputs["legacy_body"] = TranslateRequestWithAPIKeyModelCompatibility(t.Context(), headers, &config.Config{}, route.from, route.to, "model", payload, false, compat)
							outputs["legacy_update_intent"], _ = TranslateRequestWithAPIKeyModelCompatibilityChecked(t.Context(), headers, &config.Config{}, route.from, route.to, "model", payload, false, compat)
						}
						wantType := "number"
						if ua == "codex_cli_rs/0.1" && !target.preserve {
							wantType = "integer"
						}
						for entry, body := range outputs {
							if got := gjson.GetBytes(body, route.properties+".yield_time_ms.type").String(); got != wantType {
								t.Errorf("%s: yield_time_ms.type = %q, want %q; body=%s", entry, got, wantType, body)
							}
							if got := gjson.GetBytes(body, route.properties+".unrelated.type").String(); got != "number" {
								t.Errorf("%s: unrelated.type = %q, want number", entry, got)
							}
						}
						if string(payload) != route.payload {
							t.Error("source payload was mutated")
						}
						if ua != "" && (headers.Get("User-Agent") != ua || headers.Get("X-Openai-Subagent") != "collab_spawn") {
							t.Error("request headers were mutated")
						}
					})
				}
			}
		}
	}
}
