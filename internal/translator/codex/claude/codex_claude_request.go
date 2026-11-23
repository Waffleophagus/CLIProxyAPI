// Package claude provides request translation functionality for Claude Code API compatibility.
// It handles parsing and transforming Claude Code API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude Code API format and the internal client's expected format.
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToCodex parses and transforms a Claude Code API request into the internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
// The function performs the following transformations:
// 1. Sets up a template with the model name and empty instructions field
// 2. Processes system messages and converts them to developer input content
// 3. Transforms message contents (text, image, document, tool_use, tool_result) to appropriate formats
// 4. Converts tools declarations to the expected format
// 5. Adds additional configuration parameters for the Codex API
// 6. Maps Claude thinking configuration to Codex reasoning settings
// 7. Maps Claude output_config format to Codex text format
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in internal client format
func ConvertClaudeRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToCodex(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToCodexWithCompat preserves assistant thinking blocks with
// empty or unknown-format signatures for configured compatibility endpoints.
func ConvertClaudeRequestToCodexWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToCodex(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToCodex(modelName string, inputRawJSON []byte, _ bool, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops

	template := []byte(`{"model":"","instructions":"","input":[]}`)

	rootResult := gjson.ParseBytes(rawJSON)
	mayHaveToolAddition := claudePayloadMayContainToolAddition(rawJSON)
	toolNameMap := buildReverseMapFromClaudeOriginalToShortFromResult(rootResult, mayHaveToolAddition)
	signatureTargetModel := translatorcommon.ResolveCodexReplayTargetModel(modelName, rawJSON)
	template, _ = sjson.SetBytes(template, "model", modelName)
	maxOutputTokens := rootResult.Get("max_tokens")
	if !maxOutputTokens.Exists() {
		maxOutputTokens = rootResult.Get("max_output_tokens")
	}
	if maxOutputTokens.Type == gjson.Number {
		template, _ = sjson.SetBytes(template, "max_output_tokens", maxOutputTokens.Int())
	}
	inputItems := translatorcommon.NewRawArrayItems(rootResult.Get("messages.#").Int())
	toolsResult := rootResult.Get("tools")
	var declaredToolsByName map[string]gjson.Result
	declaredToolsIndexed := false
	hasAdditionalTools := false
	emittedToolNames := make(map[string]struct{})
	if toolsResult.IsArray() {
		toolArr := toolsResult.Array()
		for i := 0; i < len(toolArr); i++ {
			tool := toolArr[i]
			if isClaudeWebSearchToolType(tool.Get("type").String()) || tool.Get("defer_loading").Bool() {
				continue
			}
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				emittedToolNames[name] = struct{}{}
			}
		}
	}

	// Process system messages and convert them to input content format.
	systemsResult := rootResult.Get("system")
	if systemsResult.Exists() {
		contentItems := make([][]byte, 0, 2)

		appendSystemText := func(text string) {
			if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
				return
			}

			content := []byte(`{"type":"input_text","text":""}`)
			content, _ = sjson.SetBytes(content, "text", text)
			contentItems = append(contentItems, content)
		}

		if systemsResult.Type == gjson.String {
			appendSystemText(systemsResult.String())
		} else if systemsResult.IsArray() {
			systemResults := systemsResult.Array()
			for i := 0; i < len(systemResults); i++ {
				systemResult := systemResults[i]
				if systemResult.Get("type").String() == "text" {
					appendSystemText(systemResult.Get("text").String())
				}
			}
		}

		if len(contentItems) > 0 {
			message := []byte(`{"type":"message","role":"developer"}`)
			message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(contentItems))
			inputItems = append(inputItems, message)
		}
	}

	// Process messages and transform their contents to appropriate formats.
	messagesResult := rootResult.Get("messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		var pendingToolUseIDs []string
		var pendingSystemReminders [][]byte

		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			messageRole := messageResult.Get("role").String()
			if messageRole == "system" {
				systemContent := messageResult.Get("content")
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(systemContent); ok {
					message := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
					message, _ = sjson.SetBytes(message, "content.0.text", reminderText)
					if len(pendingToolUseIDs) > 0 {
						pendingSystemReminders = append(pendingSystemReminders, message)
					} else {
						inputItems = append(inputItems, message)
					}
				}
				if additionalTools, ok := convertClaudeSystemAdditionalTools(systemContent, toolsResult, &declaredToolsByName, &declaredToolsIndexed, toolNameMap, emittedToolNames); ok {
					hasAdditionalTools = true
					if len(pendingToolUseIDs) > 0 {
						pendingSystemReminders = append(pendingSystemReminders, additionalTools)
					} else {
						inputItems = append(inputItems, additionalTools)
					}
				}
				continue
			}

			messageContentsResult := messageResult.Get("content")
			if messageRole == "user" && len(pendingToolUseIDs) > 0 && messageContentsResult.IsArray() {
				messageContentsResult = translatorcommon.AlignClaudeToolResults(messageContentsResult, pendingToolUseIDs)
			}
			pendingToolUseIDs = nil
			contentItems := make([][]byte, 0, 4)
			// Counts only what this turn itself sends, not system reminders flushed beside it.
			// Empty text is forwarded but does not count: it can hide an emptied attachment.
			turnSendable := 0
			bufferedSendable := 0
			var turnDiscoveredTools [][]byte

			appendDiscoveredToolByReference := func(refName string) {
				refName = strings.TrimSpace(refName)
				if refName == "" {
					return
				}
				ensureClaudeDeclaredToolsIndexed(toolsResult, &declaredToolsByName, &declaredToolsIndexed)
				if declaredToolsByName == nil {
					return
				}
				def, exists := declaredToolsByName[refName]
				if !exists || !def.IsObject() || isClaudeWebSearchToolType(def.Get("type").String()) {
					return
				}
				origName := strings.TrimSpace(def.Get("name").String())
				if origName == "" {
					return
				}
				if _, alreadyEmitted := emittedToolNames[origName]; alreadyEmitted {
					return
				}
				emittedToolNames[origName] = struct{}{}
				turnDiscoveredTools = append(turnDiscoveredTools, convertClaudeFunctionToolToCodex(def, toolNameMap))
			}

			flushTurnDiscoveredTools := func() {
				if len(turnDiscoveredTools) == 0 {
					return
				}
				hasAdditionalTools = true
				turnSendable++
				itemDoc := []byte(`{"type":"additional_tools","role":"developer","tools":[]}`)
				itemDoc, _ = sjson.SetRawBytes(itemDoc, "tools", translatorcommon.JoinRawArray(turnDiscoveredTools))
				inputItems = append(inputItems, itemDoc)
				turnDiscoveredTools = turnDiscoveredTools[:0]
			}

			flushMessage := func() {
				if len(contentItems) > 0 {
					turnSendable += bufferedSendable
					bufferedSendable = 0
					message := []byte(`{"type":"message","role":""}`)
					message, _ = sjson.SetBytes(message, "role", messageRole)
					message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(contentItems))
					inputItems = append(inputItems, message)
					contentItems = contentItems[:0]
				}
			}

			appendTextContent := func(text string) {
				partType := "input_text"
				if messageRole == "assistant" {
					partType = "output_text"
				}
				content := []byte(`{"type":"","text":""}`)
				content, _ = sjson.SetBytes(content, "type", partType)
				content, _ = sjson.SetBytes(content, "text", text)
				contentItems = append(contentItems, content)
				if text != "" {
					bufferedSendable++
				}
			}

			appendImageContent := func(content []byte) {
				contentItems = append(contentItems, content)
				bufferedSendable++
			}

			appendDocumentContent := func(content []byte) {
				contentItems = append(contentItems, content)
				bufferedSendable++
			}

			appendCommentaryContent := func(part gjson.Result) {
				if messageRole != "assistant" {
					return
				}
				text := part.Get("thinking").String()
				if text == "" {
					return
				}
				// A relayed commentary block becomes its own assistant message item
				// carrying phase "commentary", mirroring the Codex output item it
				// came from. The internal marker is consumed here and never
				// forwarded upstream.
				flushMessage()
				content := []byte(`{"type":"output_text","text":""}`)
				content, _ = sjson.SetBytes(content, "text", text)
				message := []byte(`{"type":"message","role":"assistant","phase":"commentary"}`)
				message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray([][]byte{content}))
				inputItems = append(inputItems, message)
			}

			appendReasoningContent := func(part gjson.Result) {
				if messageRole != "assistant" {
					return
				}

				rawSignature := part.Get("signature").String()
				if sigcompat.IsCodexCommentarySignatureMarker(rawSignature) {
					appendCommentaryContent(part)
					return
				}
				signature, ok := translatorcommon.CodexReplaySignatureForTarget(signatureTargetModel, rawSignature)
				if !ok && preserveEmptyThinkingBlocks && part.Get("signature").Type == gjson.String && strings.TrimSpace(rawSignature) != "" &&
					sigcompat.DetectSignatureProviderForBlock(rawSignature, sigcompat.SignatureBlockKindClaudeThinking) == sigcompat.SignatureProviderUnknown {
					signature = rawSignature
					ok = true
				}
				if !ok {
					if preserveEmptyThinkingBlocks && strings.TrimSpace(rawSignature) == "" {
						signature = rawSignature
					} else {
						return
					}
				}

				flushMessage()
				reasoningItem := []byte(`{"type":"reasoning","summary":[],"content":null}`)
				reasoningItem, _ = sjson.SetBytes(reasoningItem, "encrypted_content", signature)
				inputItems = append(inputItems, reasoningItem)
			}

			if messageContentsResult.IsArray() {
				messageContentResults := messageContentsResult.Array()
				for j := 0; j < len(messageContentResults); j++ {
					messageContentResult := messageContentResults[j]
					contentType := messageContentResult.Get("type").String()

					switch contentType {
					case "text":
						flushTurnDiscoveredTools()
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						appendTextContent(messageContentResult.Get("text").String())
					case "thinking":
						flushTurnDiscoveredTools()
						appendReasoningContent(messageContentResult)
					case "image":
						flushTurnDiscoveredTools()
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						if imagePart, ok := claudeImageInputPart(messageContentResult.Get("source")); ok {
							appendImageContent(imagePart)
						} else if messageRole == "user" {
							drops.Drop(contentType)
						}
					case "document", "container_upload":
						flushTurnDiscoveredTools()
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						if contentType == "document" {
							if docText, ok := translatorcommon.ClaudeDocumentTextBlock(messageContentResult); ok {
								appendTextContent(docText)
								break
							}
							var onImageDrop func()
							if messageRole == "user" {
								onImageDrop = func() { drops.Drop("image") }
							}
							if translatorcommon.ExpandClaudeContentDocument(
								messageContentResult,
								appendTextContent,
								func(img gjson.Result) ([]byte, bool) {
									return claudeImageInputPart(img.Get("source"))
								},
								appendImageContent,
								onImageDrop,
							) {
								break
							}
						}
						if filePart, ok := claudeDocumentInputFile(messageContentResult); ok {
							appendDocumentContent(filePart)
						} else if messageRole == "user" {
							drops.Drop(contentType)
						}
					case "tool_use":
						flushTurnDiscoveredTools()
						flushMessage()
						if id := messageContentResult.Get("id").String(); id != "" {
							pendingToolUseIDs = append(pendingToolUseIDs, id)
						}
						functionCallMessage := []byte(`{"type":"function_call"}`)
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "call_id", shortenCodexCallIDIfNeeded(messageContentResult.Get("id").String()))
						{
							name := messageContentResult.Get("name").String()
							if short, ok := toolNameMap[name]; ok {
								name = short
							} else {
								name = shortenNameIfNeeded(name)
							}
							functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "name", name)
						}
						arguments := strings.TrimSpace(messageContentResult.Get("input").Raw)
						if arguments == "" {
							arguments = "{}"
						}
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "arguments", arguments)
						inputItems = append(inputItems, functionCallMessage)
					case "tool_reference":
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						flushMessage()
						refName := messageContentResult.Get("tool_name").String()
						if refName == "" {
							refName = messageContentResult.Get("name").String()
						}
						appendDiscoveredToolByReference(refName)
					case "tool_search_tool_result":
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						flushMessage()
						toolRefs := messageContentResult.Get("content.tool_references")
						if toolRefs.IsArray() {
							for _, refPart := range toolRefs.Array() {
								if refPart.Get("type").String() != "tool_reference" {
									continue
								}
								refName := refPart.Get("tool_name").String()
								if refName == "" {
									refName = refPart.Get("name").String()
								}
								appendDiscoveredToolByReference(refName)
							}
						}
					case "tool_result":
						flushMessage()
						functionCallOutputMessage := []byte(`{"type":"function_call_output"}`)
						functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "call_id", shortenCodexCallIDIfNeeded(messageContentResult.Get("tool_use_id").String()))

						contentResult := messageContentResult.Get("content")
						if contentResult.IsArray() {
							contentResults := contentResult.Array()
							toolResultContentItems := make([][]byte, 0, len(contentResults))
							hasRichToolResultItems := false
							rewroteToolRefName := false
							var rewrittenToolRefs [][]byte
							for k := 0; k < len(contentResults); k++ {
								part := contentResults[k]
								toolResultContentType := part.Get("type").String()
								if toolResultContentType == "image" {
									if imagePart, ok := claudeImageInputPart(part.Get("source")); ok {
										toolResultContentItems = append(toolResultContentItems, imagePart)
										hasRichToolResultItems = true
									}
									if rewroteToolRefName {
										rewrittenToolRefs = append(rewrittenToolRefs, []byte(part.Raw))
									}
								} else if toolResultContentType == "document" {
									toolResultContentItems = append(toolResultContentItems, codexToolResultDocumentParts(part)...)
									hasRichToolResultItems = true
									if rewroteToolRefName {
										rewrittenToolRefs = append(rewrittenToolRefs, []byte(part.Raw))
									}
								} else if toolResultContentType == "text" {
									toolResultContent := []byte(`{"type":"input_text","text":""}`)
									toolResultContent, _ = sjson.SetBytes(toolResultContent, "text", part.Get("text").String())
									toolResultContentItems = append(toolResultContentItems, toolResultContent)
									hasRichToolResultItems = true
									if rewroteToolRefName {
										rewrittenToolRefs = append(rewrittenToolRefs, []byte(part.Raw))
									}
								} else if toolResultContentType == "tool_reference" {
									refField := "tool_name"
									refName := strings.TrimSpace(part.Get("tool_name").String())
									if refName == "" {
										refField = "name"
										refName = strings.TrimSpace(part.Get("name").String())
									}
									appendDiscoveredToolByReference(refName)
									refBytes := []byte(part.Raw)
									if short, ok := toolNameMap[refName]; ok && short != refName {
										if ref, errSet := sjson.SetBytes(refBytes, refField, short); errSet == nil {
											refBytes = ref
											if !rewroteToolRefName {
												rewroteToolRefName = true
												rewrittenToolRefs = make([][]byte, 0, len(contentResults))
												for prev := 0; prev < k; prev++ {
													rewrittenToolRefs = append(rewrittenToolRefs, []byte(contentResults[prev].Raw))
												}
											}
										}
									}
									if rewroteToolRefName {
										rewrittenToolRefs = append(rewrittenToolRefs, refBytes)
									}
									toolResultRefContent := []byte(`{"type":"input_text","text":""}`)
									toolResultRefContent, _ = sjson.SetBytes(toolResultRefContent, "text", string(refBytes))
									toolResultContentItems = append(toolResultContentItems, toolResultRefContent)
								} else if rewroteToolRefName {
									rewrittenToolRefs = append(rewrittenToolRefs, []byte(part.Raw))
								}
							}
							if hasRichToolResultItems {
								functionCallOutputMessage, _ = sjson.SetRawBytes(functionCallOutputMessage, "output", translatorcommon.JoinRawArray(toolResultContentItems))
							} else if rewroteToolRefName && len(rewrittenToolRefs) == len(contentResults) {
								functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", string(translatorcommon.JoinRawArray(rewrittenToolRefs)))
							} else {
								functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
							}
						} else if contentResult.IsObject() {
							switch contentResult.Get("type").String() {
							case "image":
								if imagePart, ok := claudeImageInputPart(contentResult.Get("source")); ok {
									functionCallOutputMessage, _ = sjson.SetRawBytes(functionCallOutputMessage, "output", translatorcommon.JoinRawArray([][]byte{imagePart}))
								} else {
									functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
								}
							case "document":
								functionCallOutputMessage, _ = sjson.SetRawBytes(functionCallOutputMessage, "output", translatorcommon.JoinRawArray(codexToolResultDocumentParts(contentResult)))
							default:
								functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
							}
						} else {
							functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
						}

						inputItems = append(inputItems, functionCallOutputMessage)
						turnSendable++
					}
				}
				flushMessage()
				flushTurnDiscoveredTools()
				if len(pendingSystemReminders) > 0 {
					inputItems = append(inputItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}
			} else if messageContentsResult.Type == gjson.String {
				appendTextContent(messageContentsResult.String())
				flushMessage()
				if len(pendingSystemReminders) > 0 {
					inputItems = append(inputItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}
			}
			if messageRole == "user" {
				drops.EndTurn(turnSendable)
			}
		}

		if len(pendingSystemReminders) > 0 {
			inputItems = append(inputItems, pendingSystemReminders...)
		}
	}

	// Convert tools declarations to the expected format for the Codex API.
	hasWebSearchTool := false
	var toolItems [][]byte
	if toolsResult.IsArray() || hasAdditionalTools {
		webSearchToolNames := buildClaudeWebSearchToolNameSet(toolsResult)
		template, _ = sjson.SetRawBytes(template, "tool_choice", convertClaudeToolChoiceToCodex(rootResult.Get("tool_choice"), toolNameMap, webSearchToolNames))
	}
	if toolsResult.IsArray() {
		forcedToolName := ""
		if tc := rootResult.Get("tool_choice"); tc.IsObject() && tc.Get("type").String() == "tool" {
			forcedToolName = strings.TrimSpace(tc.Get("name").String())
		}
		toolResults := toolsResult.Array()
		toolItems = make([][]byte, 0, len(toolResults))
		for i := 0; i < len(toolResults); i++ {
			toolResult := toolResults[i]
			// Special handling: map Claude web search tool to Codex web_search
			if isClaudeWebSearchToolType(toolResult.Get("type").String()) {
				hasWebSearchTool = true
				toolItems = append(toolItems, convertClaudeWebSearchToolToCodex(toolResult))
				continue
			}
			if toolResult.Get("defer_loading").Bool() {
				name := strings.TrimSpace(toolResult.Get("name").String())
				if _, alreadyEmitted := emittedToolNames[name]; alreadyEmitted || forcedToolName == "" || name != forcedToolName {
					continue
				}
			}
			toolItems = append(toolItems, convertClaudeFunctionToolToCodex(toolResult, toolNameMap))
		}
	}

	// Default to parallel tool calls unless tool_choice explicitly disables them.
	parallelToolCalls := true
	if disableParallelToolUse := rootResult.Get("tool_choice.disable_parallel_tool_use"); disableParallelToolUse.Exists() {
		parallelToolCalls = !disableParallelToolUse.Bool()
	}

	// Add additional configuration parameters for the Codex API.
	template, _ = sjson.SetBytes(template, "parallel_tool_calls", parallelToolCalls)

	// Convert Claude thinking budgets and effort to reasoning.effort.
	reasoningEffort := "medium"
	effort := ""
	if v := rootResult.Get("output_config.effort"); v.Exists() && v.Type == gjson.String {
		effort = strings.ToLower(strings.TrimSpace(v.String()))
	}
	if thinkingConfig := rootResult.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		switch thinkingConfig.Get("type").String() {
		case "enabled":
			if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
				budget := int(budgetTokens.Int())
				if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
					reasoningEffort = effort
				}
			}
		case "adaptive", "auto":
			// Adaptive thinking can carry an explicit effort in output_config.effort (Claude 4.6).
			// Pass through directly; ApplyThinking handles clamping to target model's levels.
			if effort != "" {
				reasoningEffort = effort
			} else {
				reasoningEffort = string(thinking.LevelXHigh)
			}
		case "disabled":
			if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
				reasoningEffort = effort
			}
		}
	} else if !thinkingConfig.Exists() && effort != "" {
		reasoningEffort = effort
	}
	template, _ = sjson.SetBytes(template, "reasoning.effort", reasoningEffort)
	// OpenAI documents reasoning summaries as explicit opt-in output. Leave
	// reasoning.summary to the source request's canonical summary intent instead
	// of coupling it to reasoning effort.
	serviceTier := normalizeCodexServiceTier(rootResult.Get("service_tier"))
	if speed := rootResult.Get("speed"); speed.Type == gjson.String && speed.String() == "fast" {
		serviceTier = "priority"
	}
	if serviceTier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", serviceTier)
	}
	template, _ = sjson.SetBytes(template, "stream", true)
	template, _ = sjson.SetBytes(template, "store", false)
	includeFields := []string{"reasoning.encrypted_content"}
	if hasWebSearchTool {
		includeFields = append(includeFields, "web_search_call.action.sources")
	}
	template, _ = sjson.SetBytes(template, "include", includeFields)

	// Map Claude output_config.format to Codex Responses text.format.
	if format := rootResult.Get("output_config.format"); format.IsObject() && format.Get("type").String() == "json_schema" && format.Get("schema").IsObject() {
		name := "cli_proxy_structured_output"
		if n := format.Get("name").String(); n != "" {
			name = n
		}
		strict := true
		if s := format.Get("strict"); s.Exists() && s.Type == gjson.False {
			strict = false
		}
		// OpenAI strict mode requires every declared property to be listed
		// in required. Downgrade instead of emitting an unsatisfiable
		// strict schema that strict backends reject with HTTP 400.
		if strict && codexSchemaMissesRequired(format.Get("schema")) {
			strict = false
		}
		translatedFormat := []byte(`{"type":"json_schema","name":"","strict":true,"schema":{}}`)
		translatedFormat, _ = sjson.SetBytes(translatedFormat, "name", name)
		translatedFormat, _ = sjson.SetBytes(translatedFormat, "strict", strict)
		translatedFormat, _ = sjson.SetRawBytes(translatedFormat, "schema", []byte(format.Get("schema").Raw))
		template, _ = sjson.SetRawBytes(template, "text.format", translatedFormat)
	}

	if toolsResult.IsArray() {
		template, _ = sjson.SetRawBytes(template, "tools", translatorcommon.JoinRawArray(toolItems))
	}
	template = translatorcommon.SetRawArrayItems(template, "input", inputItems)

	return template, drops.Err()
}

func normalizeCodexServiceTier(result gjson.Result) string {
	if !result.Exists() || result.Type != gjson.String {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(result.String())) {
	case "fast", "priority":
		return "priority"
	default:
		return ""
	}
}

// shortenCodexCallIDIfNeeded keeps Claude tool IDs within the OpenAI Responses
// API call_id limit while preserving a stable, low-collision mapping.
func shortenCodexCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func isClaudeWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func buildClaudeWebSearchToolNameSet(tools gjson.Result) map[string]struct{} {
	names := map[string]struct{}{}
	if !tools.IsArray() {
		return names
	}

	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !isClaudeWebSearchToolType(toolType) {
			return true
		}

		if name := tool.Get("name").String(); name != "" {
			names[name] = struct{}{}
		}
		return true
	})

	return names
}

func convertClaudeToolChoiceToCodex(toolChoice gjson.Result, toolNameMap map[string]string, webSearchToolNames map[string]struct{}) []byte {
	if !toolChoice.Exists() || toolChoice.Type == gjson.Null {
		return []byte(`"auto"`)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType == "" && toolChoice.Type == gjson.String {
		choiceType = toolChoice.String()
	}

	switch choiceType {
	case "auto", "":
		return []byte(`"auto"`)
	case "any":
		return []byte(`"required"`)
	case "none":
		return []byte(`"none"`)
	case "tool":
		name := toolChoice.Get("name").String()
		if _, ok := webSearchToolNames[name]; ok {
			return []byte(`{"type":"web_search"}`)
		}
		if short, ok := toolNameMap[name]; ok {
			name = short
		} else {
			name = shortenNameIfNeeded(name)
		}
		if name == "" {
			return []byte(`"auto"`)
		}

		choice := []byte(`{"type":"function","name":""}`)
		choice, _ = sjson.SetBytes(choice, "name", name)
		return choice
	default:
		return []byte(`"auto"`)
	}
}

func convertClaudeWebSearchToolToCodex(tool gjson.Result) []byte {
	out := []byte(`{"type":"web_search"}`)
	if allowedDomains := tool.Get("allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		out, _ = sjson.SetRawBytes(out, "filters.allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		out, _ = sjson.SetRawBytes(out, "user_location", []byte(userLocation.Raw))
	}
	return out
}

// gatewayToolNamePattern matches gateway tool ids of the form
// `[mcp_]<server words>-<hex id...>_<tool>`, e.g.
// `mcp_github-12345678-9abc-def0-1234-56789abcdef0-12345678_add-issue-comment`.
var gatewayToolNamePattern = regexp.MustCompile(`^(?:mcp_)?([a-z][a-z0-9-]*?)-[0-9a-f]{6,}(?:-[0-9a-f]+)*_(.+)$`)

// shortenNameIfNeeded applies a simple shortening rule for a single name.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			rest := name[idx+2:]
			cand := "mcp__" + rest
			if len(cand) <= limit {
				return cand
			}
			// Plain truncation keeps only the shared server prefix, so every tool of a
			// long-named server collapses to the same 64 characters and the model cannot
			// tell them apart. Keep the server's words and the tool's own name instead.
			if m := gatewayToolNamePattern.FindStringSubmatch(rest); m != nil {
				if readable := "mcp__" + m[1] + "_" + m[2]; len(readable) <= limit {
					return readable
				}
			}
			return cand[:limit]
		}
	}
	return name[:limit]
}

// buildShortNameMap ensures uniqueness of shortened names within a request.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := shortenNameIfNeeded

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		if _, exists := m[n]; exists {
			continue
		}
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

func convertClaudeFunctionToolToCodex(toolResult gjson.Result, toolNameMap map[string]string) []byte {
	tool := []byte(toolResult.Raw)
	if toolResult.Get("type").Type != gjson.String || toolResult.Get("type").String() != "function" {
		tool, _ = sjson.SetBytes(tool, "type", "function")
	}
	// Apply shortened name if needed
	if v := toolResult.Get("name"); v.Exists() {
		originalName := v.String()
		name := originalName
		if short, ok := toolNameMap[name]; ok {
			name = short
		} else {
			name = shortenNameIfNeeded(name)
		}
		if v.Type != gjson.String || name != originalName {
			tool, _ = sjson.SetBytes(tool, "name", name)
		}
	}
	tool, _ = sjson.SetRawBytes(tool, "parameters", []byte(normalizeToolParameters(toolResult.Get("input_schema").Raw)))
	for _, path := range []string{"input_schema", "parameters.$schema", "cache_control", "defer_loading"} {
		if gjson.GetBytes(tool, path).Exists() {
			tool, _ = sjson.DeleteBytes(tool, path)
		}
	}
	if gjson.GetBytes(tool, "strict").Type != gjson.False {
		tool, _ = sjson.SetBytes(tool, "strict", false)
	}
	return tool
}

func ensureClaudeDeclaredToolsIndexed(toolsResult gjson.Result, declaredToolsByName *map[string]gjson.Result, declaredToolsIndexed *bool) {
	if *declaredToolsIndexed {
		return
	}
	*declaredToolsIndexed = true
	if !toolsResult.IsArray() {
		return
	}
	toolArr := toolsResult.Array()
	if *declaredToolsByName == nil {
		*declaredToolsByName = make(map[string]gjson.Result, len(toolArr))
	}
	for i := 0; i < len(toolArr); i++ {
		if name := strings.TrimSpace(toolArr[i].Get("name").String()); name != "" {
			if _, exists := (*declaredToolsByName)[name]; !exists {
				(*declaredToolsByName)[name] = toolArr[i]
			}
		}
	}
}

// convertClaudeSystemAdditionalTools converts mid-conversation tool_addition blocks
// inside a role=system message into a Responses additional_tools input item.
func convertClaudeSystemAdditionalTools(content, toolsResult gjson.Result, declaredToolsByName *map[string]gjson.Result, declaredToolsIndexed *bool, toolNameMap map[string]string, emittedToolNames map[string]struct{}) ([]byte, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := content.Array()
	var toolItems [][]byte
	var indexByName map[string]int
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part.Get("type").String() != "tool_addition" {
			continue
		}
		toolSpec := part.Get("tool")
		var def gjson.Result
		isReference := false
		switch toolSpec.Get("type").String() {
		case "tool_definition":
			def = toolSpec.Get("definition")
			if def.IsObject() {
				if name := def.Get("name").String(); name != "" {
					ensureClaudeDeclaredToolsIndexed(toolsResult, declaredToolsByName, declaredToolsIndexed)
					if *declaredToolsByName == nil {
						*declaredToolsByName = make(map[string]gjson.Result)
					}
					(*declaredToolsByName)[name] = def
				}
			}
		case "tool_reference":
			isReference = true
			ensureClaudeDeclaredToolsIndexed(toolsResult, declaredToolsByName, declaredToolsIndexed)
			refName := toolSpec.Get("name").String()
			if refName == "" {
				refName = toolSpec.Get("tool_name").String()
			}
			if refName != "" && *declaredToolsByName != nil {
				def = (*declaredToolsByName)[refName]
			}
		default:
			if candidate := toolSpec.Get("definition"); candidate.IsObject() {
				def = candidate
				if name := def.Get("name").String(); name != "" {
					ensureClaudeDeclaredToolsIndexed(toolsResult, declaredToolsByName, declaredToolsIndexed)
					if *declaredToolsByName == nil {
						*declaredToolsByName = make(map[string]gjson.Result)
					}
					(*declaredToolsByName)[name] = def
				}
			}
		}
		if !def.IsObject() || isClaudeWebSearchToolType(def.Get("type").String()) {
			continue
		}
		origName := strings.TrimSpace(def.Get("name").String())
		if origName == "" {
			continue
		}
		if isReference && emittedToolNames != nil {
			if _, alreadyEmitted := emittedToolNames[origName]; alreadyEmitted {
				continue
			}
		}
		if emittedToolNames != nil {
			emittedToolNames[origName] = struct{}{}
		}
		converted := convertClaudeFunctionToolToCodex(def, toolNameMap)
		if indexByName == nil {
			indexByName = make(map[string]int)
		}
		if idx, exists := indexByName[origName]; exists {
			toolItems[idx] = converted
		} else {
			indexByName[origName] = len(toolItems)
			toolItems = append(toolItems, converted)
		}
	}
	if len(toolItems) == 0 {
		return nil, false
	}
	itemDoc := []byte(`{"type":"additional_tools","role":"developer","tools":[]}`)
	itemDoc, _ = sjson.SetRawBytes(itemDoc, "tools", translatorcommon.JoinRawArray(toolItems))
	return itemDoc, true
}

func collectClaudeDeclaredToolNames(rootResult gjson.Result, hasDynamicTools bool) []string {
	var names []string
	var deferredNames []string
	var deferredSet map[string]struct{}
	tools := rootResult.Get("tools")
	if tools.IsArray() {
		arr := tools.Array()
		names = make([]string, 0, len(arr))
		for i := 0; i < len(arr); i++ {
			n := arr[i].Get("name").String()
			if n == "" {
				continue
			}
			if hasDynamicTools && arr[i].Get("defer_loading").Bool() {
				deferredNames = append(deferredNames, n)
				if deferredSet == nil {
					deferredSet = make(map[string]struct{})
				}
				deferredSet[n] = struct{}{}
				continue
			}
			names = append(names, n)
		}
	}
	if !hasDynamicTools {
		return names
	}
	appendDeferredRef := func(refName string) {
		refName = strings.TrimSpace(refName)
		if refName == "" || deferredSet == nil {
			return
		}
		if _, ok := deferredSet[refName]; ok {
			names = append(names, refName)
		}
	}
	messages := rootResult.Get("messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			content := msg.Get("content")
			if !content.IsArray() {
				continue
			}
			if msg.Get("role").String() == "system" {
				for _, part := range content.Array() {
					if part.Get("type").String() != "tool_addition" {
						continue
					}
					tool := part.Get("tool")
					switch tool.Get("type").String() {
					case "tool_definition":
						def := tool.Get("definition")
						if isClaudeWebSearchToolType(def.Get("type").String()) {
							continue
						}
						if n := def.Get("name").String(); n != "" {
							names = append(names, n)
						}
					case "tool_reference":
						refName := tool.Get("name").String()
						if refName == "" {
							refName = tool.Get("tool_name").String()
						}
						appendDeferredRef(refName)
					default:
						if def := tool.Get("definition"); def.IsObject() && !isClaudeWebSearchToolType(def.Get("type").String()) {
							if n := def.Get("name").String(); n != "" {
								names = append(names, n)
							}
						}
					}
				}
				continue
			}
			for _, part := range content.Array() {
				switch part.Get("type").String() {
				case "tool_reference":
					refName := part.Get("tool_name").String()
					if refName == "" {
						refName = part.Get("name").String()
					}
					appendDeferredRef(refName)
				case "tool_search_tool_result":
					toolRefs := part.Get("content.tool_references")
					if toolRefs.IsArray() {
						for _, refPart := range toolRefs.Array() {
							if refPart.Get("type").String() != "tool_reference" {
								continue
							}
							refName := refPart.Get("tool_name").String()
							if refName == "" {
								refName = refPart.Get("name").String()
							}
							appendDeferredRef(refName)
						}
					}
				case "tool_result":
					nested := part.Get("content")
					if !nested.IsArray() {
						continue
					}
					for _, nestedPart := range nested.Array() {
						if nestedPart.Get("type").String() != "tool_reference" {
							continue
						}
						refName := nestedPart.Get("tool_name").String()
						if refName == "" {
							refName = nestedPart.Get("name").String()
						}
						appendDeferredRef(refName)
					}
				}
			}
		}
	}
	names = append(names, deferredNames...)
	return names
}

func buildReverseMapFromClaudeOriginalToShortFromResult(rootResult gjson.Result, hasDynamicTools bool) map[string]string {
	names := collectClaudeDeclaredToolNames(rootResult, hasDynamicTools)
	if len(names) == 0 {
		return map[string]string{}
	}
	return buildShortNameMap(names)
}

func claudePayloadMayContainToolAddition(rawJSON []byte) bool {
	return bytes.Contains(rawJSON, []byte("tool_addition")) ||
		bytes.Contains(rawJSON, []byte("defer_loading")) ||
		bytes.Contains(rawJSON, []byte("tool_reference")) ||
		bytes.Contains(rawJSON, []byte(`\u`)) ||
		bytes.Contains(rawJSON, []byte(`\U`))
}

// buildReverseMapFromClaudeOriginalToShort builds original->short map, used to map tool_use names to short.
func buildReverseMapFromClaudeOriginalToShort(original []byte) map[string]string {
	mayHaveToolAddition := claudePayloadMayContainToolAddition(original)
	if !mayHaveToolAddition {
		tools := gjson.GetBytes(original, "tools")
		m := map[string]string{}
		if !tools.IsArray() {
			return m
		}
		var names []string
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			n := arr[i].Get("name").String()
			if n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			m = buildShortNameMap(names)
		}
		return m
	}
	return buildReverseMapFromClaudeOriginalToShortFromResult(gjson.ParseBytes(original), true)
}

// normalizeToolParameters ensures object schemas contain at least an empty properties map,
// strips dialect keywords ($schema, $id), and drops regex patterns containing unsupported
// Unicode property escapes (\p{...} / \P{...}) that cause upstream schema validation failures.
func normalizeToolParameters(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return `{"type":"object","properties":{}}`
	}
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&root); errDecode != nil || root == nil {
		return `{"type":"object","properties":{}}`
	}

	stripDialectKeywordsFromSchema(root)

	typeVal, typeExists := root["type"]
	isObject := false
	if !typeExists || typeVal == nil || typeVal == "" {
		root["type"] = "object"
		isObject = true
	} else {
		switch t := typeVal.(type) {
		case string:
			if t == "object" {
				isObject = true
			}
		case []any:
			for _, elem := range t {
				if elemStr, ok := elem.(string); ok && elemStr == "object" {
					isObject = true
					break
				}
			}
		}
	}
	if isObject {
		if props, ok := root["properties"]; !ok || props == nil {
			root["properties"] = map[string]any{}
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(root); errEncode != nil {
		return `{"type":"object","properties":{}}`
	}
	return strings.TrimSpace(buf.String())
}

func stripDialectKeywordsFromSchema(v any) {
	switch schema := v.(type) {
	case map[string]any:
		delete(schema, "$schema")
		delete(schema, "$id")
		if patternVal, ok := schema["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(schema, "pattern")
		}

		// Inspect regex keys under patternProperties
		if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
				} else {
					stripDialectKeywordsFromSchema(subSchema)
				}
			}
		}

		for _, mapKey := range codexSchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := schema[mapKey].(map[string]any); ok {
				for _, subSchema := range subMap {
					stripDialectKeywordsFromSchema(subSchema)
				}
			}
		}

		for _, valKey := range codexSchemaValueKeywords {
			if val, exists := schema[valKey]; exists {
				switch sub := val.(type) {
				case map[string]any:
					stripDialectKeywordsFromSchema(sub)
				case []any:
					for _, item := range sub {
						stripDialectKeywordsFromSchema(item)
					}
				}
			}
		}
	case []any:
		for _, item := range schema {
			stripDialectKeywordsFromSchema(item)
		}
	}
}

// codexSchemaMapKeywords and codexSchemaValueKeywords reference the unified JSON Schema keywords
// declared in internal/util.
var (
	codexSchemaMapKeywords   = util.SchemaMapKeywords
	codexSchemaValueKeywords = util.SchemaValueKeywords
)

// codexSchemaMissesRequired reports whether a JSON Schema has any declared
// property missing from its sibling required list (recursively). OpenAI
// strict mode rejects such schemas with HTTP 400, so callers downgrade
// text.format.strict instead of emitting an unsatisfiable schema.
func codexSchemaMissesRequired(schema gjson.Result) bool {
	if !schema.IsObject() {
		if schema.IsArray() {
			miss := false
			schema.ForEach(func(_, child gjson.Result) bool {
				if codexSchemaMissesRequired(child) {
					miss = true
					return false
				}
				return true
			})
			return miss
		}
		return false
	}
	if properties := schema.Get("properties"); properties.IsObject() {
		required := schema.Get("required")
		if !required.IsArray() {
			return len(properties.Map()) > 0
		}
		names := make(map[string]struct{}, len(required.Array()))
		for _, item := range required.Array() {
			if item.Type == gjson.String {
				names[item.String()] = struct{}{}
			}
		}
		for name := range properties.Map() {
			if _, ok := names[name]; !ok {
				return true
			}
		}
	}
	for _, keyword := range codexSchemaMapKeywords {
		children := schema.Get(keyword)
		if !children.IsObject() {
			continue
		}
		miss := false
		children.ForEach(func(_, child gjson.Result) bool {
			if codexSchemaMissesRequired(child) {
				miss = true
				return false
			}
			return true
		})
		if miss {
			return true
		}
	}
	for _, keyword := range codexSchemaValueKeywords {
		if child := schema.Get(keyword); child.Exists() && codexSchemaMissesRequired(child) {
			return true
		}
	}
	return false
}

// claudeDocumentDataURL turns an inline PDF into a Codex input_file URL. Anything
// else, a file id included, is not representable and is reported by the caller.
// claudeImageInputPart maps a Claude image source onto a Responses input_image
// part. Base64 bytes become a data URL, an http(s) url passes through and a file
// id is carried as file_id. It reports false for a source with none of them.
func claudeImageInputPart(source gjson.Result) ([]byte, bool) {
	if !source.Exists() {
		return nil, false
	}
	part := []byte(`{"type":"input_image"}`)
	data := source.Get("data").String()
	if data == "" {
		data = source.Get("base64").String()
	}
	switch {
	case data != "":
		mediaType := source.Get("media_type").String()
		if mediaType == "" {
			mediaType = source.Get("mime_type").String()
		}
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		part, _ = sjson.SetBytes(part, "image_url", fmt.Sprintf("data:%s;base64,%s", mediaType, data))
	case source.Get("type").String() == "url" && translatorcommon.IsHTTPURL(source.Get("url").String()):
		part, _ = sjson.SetBytes(part, "image_url", strings.TrimSpace(source.Get("url").String()))
	case source.Get("type").String() == "file" && source.Get("file_id").String() != "":
		part, _ = sjson.SetBytes(part, "file_id", source.Get("file_id").String())
	default:
		return nil, false
	}
	return part, true
}

func claudeDocumentInputFile(part gjson.Result) ([]byte, bool) {
	source := part.Get("source")
	filename := strings.TrimSpace(part.Get("filename").String())
	if filename == "" {
		filename = strings.TrimSpace(part.Get("title").String())
	}
	switch source.Get("type").String() {
	case "base64":
		mediaType := strings.TrimSpace(source.Get("media_type").String())
		if !strings.EqualFold(mediaType, "application/pdf") {
			return nil, false
		}
		data := source.Get("data").String()
		if data == "" {
			data = source.Get("base64").String()
		}
		if data == "" {
			return nil, false
		}
		if filename == "" {
			filename = "document.pdf"
		}
		part := []byte(`{"type":"input_file","file_data":"","filename":""}`)
		part, _ = sjson.SetBytes(part, "file_data", fmt.Sprintf("data:%s;base64,%s", mediaType, data))
		part, _ = sjson.SetBytes(part, "filename", filename)
		return part, true
	case "url":
		fileURL := strings.TrimSpace(source.Get("url").String())
		if !translatorcommon.IsHTTPURL(fileURL) {
			return nil, false
		}
		part := []byte(`{"type":"input_file","file_url":""}`)
		part, _ = sjson.SetBytes(part, "file_url", fileURL)
		if filename != "" {
			part, _ = sjson.SetBytes(part, "filename", filename)
		}
		return part, true
	default:
		return nil, false
	}
}

const codexUnsupportedDocumentPlaceholder = "[Unsupported document content]"

func codexInputTextPart(text string) []byte {
	doc := []byte(`{"type":"input_text","text":""}`)
	doc, _ = sjson.SetBytes(doc, "text", text)
	return doc
}

func codexUnsupportedDocumentPart() []byte {
	return codexInputTextPart(codexUnsupportedDocumentPlaceholder)
}

func codexToolResultDocumentParts(part gjson.Result) [][]byte {
	if docText, ok := translatorcommon.ClaudeDocumentTextBlock(part); ok {
		return [][]byte{codexInputTextPart(docText)}
	}
	var items [][]byte
	if translatorcommon.ExpandClaudeContentDocument(
		part,
		func(text string) { items = append(items, codexInputTextPart(text)) },
		func(img gjson.Result) ([]byte, bool) {
			return claudeImageInputPart(img.Get("source"))
		},
		func(img []byte) { items = append(items, img) },
		nil,
	) {
		return items
	}
	if docPart, ok := claudeDocumentInputFile(part); ok {
		return [][]byte{docPart}
	}
	return [][]byte{codexUnsupportedDocumentPart()}
}
