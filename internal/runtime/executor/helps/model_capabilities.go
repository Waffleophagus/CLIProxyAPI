package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound authoritative model capabilities to this execution attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	var out []byte
	var err error
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		out, err = thinking.ApplyThinkingWithModelInfoAndSummary(body, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig)
	} else {
		out, err = thinking.ApplyThinkingWithSourceAndSummary(body, source, req.Model, fromFormat, toFormat, provider, summaryConfig)
	}
	if err != nil {
		return out, err
	}
	if shouldReconcileClaudeOpenAICompatToolCallReasoning(req, fromFormat, toFormat) && len(out) > 0 {
		out = reconcileClaudeOpenAICompatToolCallReasoning(out)
	}
	return out, nil
}

func shouldReconcileClaudeOpenAICompatToolCallReasoning(req cliproxyexecutor.Request, fromFormat, toFormat string) bool {
	return APIKeyModelIsCompat(req) &&
		strings.EqualFold(strings.TrimSpace(fromFormat), "claude") &&
		strings.EqualFold(strings.TrimSpace(toFormat), "openai")
}

// reconcileClaudeOpenAICompatToolCallReasoning keeps the translator's
// placeholder reasoning_content on assistant tool-call turns in line with the
// final, canonical reasoning state: compat thinking upstreams require it while
// reasoning is active, and it must not linger once reasoning was disabled.
func reconcileClaudeOpenAICompatToolCallReasoning(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	reasoningActive := false
	if effortRes := gjson.GetBytes(body, "reasoning_effort"); effortRes.Exists() {
		resolvedEffort := strings.ToLower(strings.TrimSpace(effortRes.String()))
		if resolvedEffort != "" && resolvedEffort != string(thinking.LevelNone) && resolvedEffort != "0" && resolvedEffort != "false" {
			reasoningActive = true
		}
	}

	for idx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		path := fmt.Sprintf("messages.%d.reasoning_content", idx)
		reasoning := msg.Get("reasoning_content")
		if reasoningActive {
			if !reasoning.Exists() || (strings.TrimSpace(reasoning.String()) == "" && reasoning.String() != " ") {
				if updated, errSet := sjson.SetBytes(body, path, " "); errSet == nil {
					body = updated
				}
			}
			continue
		}
		if reasoning.Exists() && strings.TrimSpace(reasoning.String()) == "" {
			if updated, errDel := sjson.DeleteBytes(body, path); errDel == nil {
				body = updated
			}
		}
	}
	return body
}
