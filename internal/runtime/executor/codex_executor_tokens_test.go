package executor

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCountTokensPreservesToolNumberSchemas(t *testing.T) {
	for _, source := range []struct {
		format  sdktranslator.Format
		payload string
	}{
		{format: sdktranslator.FormatOpenAIResponse, payload: `{"input":"hi","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`},
		{format: sdktranslator.FormatClaude, payload: `{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"exec_command","input_schema":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`},
	} {
		for _, compat := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/compat=%t", source.format, compat), func(t *testing.T) {
				exec := NewCodexExecutor(&config.Config{})
				resp, errCount := exec.CountTokens(t.Context(), nil, cliproxyexecutor.Request{
					Model:   "gpt-5.4",
					Payload: []byte(source.payload),
					Metadata: map[string]any{
						"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "gpt-5.4", IsCompat: compat},
					},
				}, cliproxyexecutor.Options{
					SourceFormat: source.format,
					Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}},
				})
				if errCount != nil {
					t.Fatalf("CountTokens() error = %v", errCount)
				}
				// Token counts alone can miss a schema rewrite: number and integer
				// may have the same token length. Inspect the translated schema too.
				body, errTranslate := helps.TranslateRequestWithAPIKeyModelCompatibilityCheckedForExecutor(t.Context(), http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}}, &config.Config{}, exec.Identifier(), source.format, sdktranslator.FormatCodex, "gpt-5.4", []byte(source.payload), false, compat)
				if errTranslate != nil {
					t.Fatalf("translate error = %v", errTranslate)
				}
				if got := gjson.GetBytes(body, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
					t.Fatalf("yield_time_ms.type = %q, want number; body=%s", got, body)
				}
				if len(resp.Payload) == 0 {
					t.Fatal("CountTokens() returned an empty response")
				}
			})
		}
	}
}
