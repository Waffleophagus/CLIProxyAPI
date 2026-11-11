package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// xAI executes these x_search subtools server-side but exposes their trace as
// client-style tool calls. Hide the trace so Responses clients do not execute it again.
type xaiInternalXSearchResponseFilter struct {
	enabled              bool
	clientDeclaredTools  map[xaiClientToolKey]struct{}
	droppedOutputIndexes map[int64]struct{}
	droppedItemIDs       map[string]struct{}
}

func newXAIInternalXSearchResponseFilter(enabled bool, clientDeclaredTools map[xaiClientToolKey]struct{}) *xaiInternalXSearchResponseFilter {
	filter := &xaiInternalXSearchResponseFilter{
		enabled:             enabled,
		clientDeclaredTools: clientDeclaredTools,
	}
	if enabled {
		filter.droppedOutputIndexes = make(map[int64]struct{})
		filter.droppedItemIDs = make(map[string]struct{})
	}
	return filter
}

func xaiRequestHasNativeXSearch(body []byte) bool {
	if gjson.GetBytes(body, `tools.#(type=="x_search")`).Exists() {
		return true
	}
	// Multipath queries return an array of matches; an empty array still Exists().
	// Check the match count instead of Exists() for additional_tools injection.
	return len(gjson.GetBytes(body, `input.#(type=="additional_tools")#.tools.#(type=="x_search")`).Array()) > 0
}

// collectXAIClientDeclaredToolKeys records client-declared function/custom tools
// using the Responses post-restore identity (short name + optional namespace) and
// the effective upstream tool type after normalizeXAITool. Client custom tools
// are normalized to function before being sent to xAI, so keys use function for
// both declaration kinds. Must run before normalizeXAITools flattens namespace wrappers.
func collectXAIClientDeclaredToolKeys(body []byte) map[xaiClientToolKey]struct{} {
	keys := make(map[xaiClientToolKey]struct{})
	collect := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			switch toolType := strings.TrimSpace(tool.Get("type").String()); toolType {
			case xaiNamespaceToolType:
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				if namespaceName == "" {
					continue
				}
				for _, nestedTool := range tool.Get("tools").Array() {
					nestedType := strings.TrimSpace(nestedTool.Get("type").String())
					if nestedType != xaiFunctionToolType && nestedType != xaiCustomToolType {
						continue
					}
					toolName := strings.TrimSpace(nestedTool.Get("name").String())
					if toolName == "" {
						continue
					}
					// normalizeXAITool converts custom → function before upstream send.
					keys[xaiClientToolKey{namespace: namespaceName, name: toolName, toolType: xaiEffectiveDeclaredToolType(nestedType)}] = struct{}{}
				}
			case xaiFunctionToolType, xaiCustomToolType:
				toolName := strings.TrimSpace(tool.Get("name").String())
				if toolName == "" {
					continue
				}
				// normalizeXAITool converts custom → function before upstream send.
				keys[xaiClientToolKey{namespace: "", name: toolName, toolType: xaiEffectiveDeclaredToolType(toolType)}] = struct{}{}
			}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return keys
}

// xaiEffectiveDeclaredToolType returns the tool type actually sent upstream
// after normalizeXAITool. Client custom tools are rewritten to function.
func xaiEffectiveDeclaredToolType(toolType string) string {
	if strings.TrimSpace(toolType) == xaiCustomToolType {
		return xaiFunctionToolType
	}
	return strings.TrimSpace(toolType)
}

func xaiIsInternalXSearchToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch":
		return true
	default:
		return false
	}
}

// xaiResponseCallDeclaredType maps a Responses output call type to the effective
// upstream tool declaration kind used when matching client-declared tools.
// Client custom tools are normalized to function before upstream send, so only
// function_call can match a client-declared same-name tool; custom_tool_call
// remains the internal X Search trace shape.
func xaiResponseCallDeclaredType(itemType string) string {
	switch strings.TrimSpace(itemType) {
	case "function_call":
		return xaiFunctionToolType
	case "custom_tool_call":
		return xaiCustomToolType
	default:
		return ""
	}
}

// xaiIsInternalXSearchCallID reports whether call_id matches the evidenced xAI
// X Search server-side trace prefix (xs_call...), as observed in Responses traffic
// for native x_search subtools (see issue #4282 / PR #4284 fixtures).
func xaiIsInternalXSearchCallID(callID string) bool {
	return strings.HasPrefix(strings.TrimSpace(callID), "xs_call")
}

// xaiIsInternalXSearchCall reports whether an output item is an xAI server-side
// X Search subtool trace that should be hidden from Responses clients.
//
// Evidence from xAI Responses traffic (issue #4282 / PR #4284):
//   - native x_search subtools are emitted as custom_tool_call items named
//     x_user_search / x_semantic_search / x_keyword_search / x_thread_fetch
//   - those traces commonly use call_id values prefixed with "xs_call"
//
// Client tools that share a short name are preserved only when the response call
// kind matches the effective upstream declaration type. Because normalizeXAITool
// rewrites client custom → function, a client custom x_keyword_search is keyed as
// function and therefore preserves function_call while still filtering genuine
// internal custom_tool_call / xs_call* traces. Namespaced restored client tools
// are never treated as internal.
func xaiIsInternalXSearchCall(item gjson.Result, clientDeclaredTools map[xaiClientToolKey]struct{}) bool {
	itemType := strings.TrimSpace(item.Get("type").String())
	declaredType := xaiResponseCallDeclaredType(itemType)
	if declaredType == "" {
		return false
	}
	name := strings.TrimSpace(item.Get("name").String())
	if !xaiIsInternalXSearchToolName(name) {
		return false
	}
	namespace := strings.TrimSpace(item.Get("namespace").String())
	// Namespaced calls are restored client tools, never xAI internal X Search traces.
	if namespace != "" {
		return false
	}
	// Evidenced internal call_id prefix always identifies server-side X Search traces,
	// even when a client tool reuses the same short name.
	if xaiIsInternalXSearchCallID(item.Get("call_id").String()) {
		return true
	}
	// Preserve only client tools whose effective upstream declaration kind matches
	// this call type (function_call ↔ function after custom normalization).
	if _, declared := clientDeclaredTools[xaiClientToolKey{namespace: namespace, name: name, toolType: declaredType}]; declared {
		return false
	}
	return true
}

func (f *xaiInternalXSearchResponseFilter) apply(eventData []byte) []byte {
	if f == nil || !f.enabled || len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return eventData
	}

	if item := gjson.GetBytes(eventData, "item"); xaiIsInternalXSearchCall(item, f.clientDeclaredTools) {
		f.recordDroppedItem(eventData, item)
		return nil
	}

	eventData = f.filterCompletedOutput(eventData)
	if f.referencesDroppedItem(eventData) {
		return nil
	}
	return f.compactOutputIndex(eventData)
}

func (f *xaiInternalXSearchResponseFilter) recordDroppedItem(eventData []byte, item gjson.Result) {
	if outputIndex := gjson.GetBytes(eventData, "output_index"); outputIndex.Exists() {
		f.droppedOutputIndexes[outputIndex.Int()] = struct{}{}
	}
	for _, path := range []string{"id", "call_id"} {
		if id := strings.TrimSpace(item.Get(path).String()); id != "" {
			f.droppedItemIDs[id] = struct{}{}
		}
	}
}

func (f *xaiInternalXSearchResponseFilter) referencesDroppedItem(eventData []byte) bool {
	if outputIndex := gjson.GetBytes(eventData, "output_index"); outputIndex.Exists() {
		if _, dropped := f.droppedOutputIndexes[outputIndex.Int()]; dropped {
			return true
		}
	}
	for _, path := range []string{"item_id", "call_id"} {
		id := strings.TrimSpace(gjson.GetBytes(eventData, path).String())
		if _, dropped := f.droppedItemIDs[id]; id != "" && dropped {
			return true
		}
	}
	return false
}

func (f *xaiInternalXSearchResponseFilter) compactOutputIndex(eventData []byte) []byte {
	outputIndex := gjson.GetBytes(eventData, "output_index")
	if !outputIndex.Exists() {
		return eventData
	}
	original := outputIndex.Int()
	removedBefore := int64(0)
	for dropped := range f.droppedOutputIndexes {
		if dropped < original {
			removedBefore++
		}
	}
	if removedBefore == 0 {
		return eventData
	}
	updated, errSet := sjson.SetBytes(eventData, "output_index", original-removedBefore)
	if errSet != nil {
		return eventData
	}
	return updated
}

func (f *xaiInternalXSearchResponseFilter) filterCompletedOutput(eventData []byte) []byte {
	output := gjson.GetBytes(eventData, "response.output")
	if !output.IsArray() {
		return eventData
	}
	var clientDeclaredTools map[xaiClientToolKey]struct{}
	if f != nil {
		clientDeclaredTools = f.clientDeclaredTools
	}
	items := make([]json.RawMessage, 0, len(output.Array()))
	changed := false
	for _, item := range output.Array() {
		if xaiIsInternalXSearchCall(item, clientDeclaredTools) {
			changed = true
			continue
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return eventData
	}
	rawOutput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return eventData
	}
	updated, errSet := sjson.SetRawBytes(eventData, "response.output", rawOutput)
	if errSet != nil {
		return eventData
	}
	return updated
}

func normalizeXAIInputNamespaceToolCalls(body []byte) []byte {
	return normalizeXAIInputNamespaceToolCallsWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func normalizeXAIInputNamespaceToolCallsWithFold(body []byte, shouldFold bool) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	for index, item := range input.Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		namespaceName := strings.TrimSpace(item.Get("namespace").String())
		toolName := strings.TrimSpace(item.Get("name").String())
		if namespaceName == "" {
			continue
		}
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
		var isFolded bool
		if xaiHasFunctionToolNamed(body, namespaceName) {
			isFolded = true
		} else if xaiHasFunctionToolNamed(body, qualifiedName) {
			isFolded = false
		} else {
			isFolded = shouldFold
		}
		if isFolded {
			namePath := fmt.Sprintf("input.%d.name", index)
			namespacePath := fmt.Sprintf("input.%d.namespace", index)
			argsPath := fmt.Sprintf("input.%d.arguments", index)

			dispatcherArgs := map[string]any{
				"name": toolName,
			}
			if rawArgs := item.Get("arguments").String(); rawArgs != "" {
				if gjson.Valid(rawArgs) {
					dispatcherArgs["arguments"] = json.RawMessage(rawArgs)
				} else {
					dispatcherArgs["arguments"] = rawArgs
				}
			}
			encodedArgs, errMarshal := json.Marshal(dispatcherArgs)
			if errMarshal != nil {
				continue
			}

			updated, errSet := sjson.SetBytes(body, namePath, namespaceName)
			if errSet != nil {
				continue
			}
			updated, errSet = sjson.SetBytes(updated, argsPath, string(encodedArgs))
			if errSet != nil {
				continue
			}
			updated, errDelete := sjson.DeleteBytes(updated, namespacePath)
			if errDelete != nil {
				continue
			}
			body = updated
			continue
		}

		if qualifiedName == "" {
			continue
		}
		namePath := fmt.Sprintf("input.%d.name", index)
		namespacePath := fmt.Sprintf("input.%d.namespace", index)
		updated, errSet := sjson.SetBytes(body, namePath, qualifiedName)
		if errSet != nil {
			continue
		}
		updated, errDelete := sjson.DeleteBytes(updated, namespacePath)
		if errDelete != nil {
			continue
		}
		body = updated
	}
	return body
}

type xaiNamespaceRestorer struct {
	refs              map[string]xaiNamespaceToolRef
	dispatcherItemIDs map[string]string
}

func newXAINamespaceRestorer(refs map[string]xaiNamespaceToolRef) *xaiNamespaceRestorer {
	return &xaiNamespaceRestorer{
		refs:              refs,
		dispatcherItemIDs: make(map[string]string),
	}
}

func (r *xaiNamespaceRestorer) restore(data []byte) []byte {
	if r == nil || len(r.refs) == 0 || len(data) == 0 || !gjson.ValidBytes(data) {
		return data
	}
	eventType := gjson.GetBytes(data, "type").String()
	switch eventType {
	case "response.output_item.added":
		item := gjson.GetBytes(data, "item")
		if item.Get("type").String() == "function_call" {
			name := strings.TrimSpace(item.Get("name").String())
			itemID := strings.TrimSpace(item.Get("id").String())
			if ref, ok := r.refs[name]; ok && ref.isDispatcher {
				if itemID != "" {
					r.dispatcherItemIDs[itemID] = ref.namespace
				}
				data, _ = sjson.SetBytes(data, "item.namespace", ref.namespace)
			}
		}
		return data

	case "response.function_call_arguments.done":
		itemID := strings.TrimSpace(gjson.GetBytes(data, "item_id").String())
		if namespaceName, isDisp := r.dispatcherItemIDs[itemID]; isDisp {
			rawArgs := gjson.GetBytes(data, "arguments").String()
			if _, childArgs, ok := unwrapXAIDispatcherArguments(rawArgs, namespaceName, r.refs); ok {
				updated, errSet := sjson.SetBytes(data, "arguments", string(childArgs))
				if errSet == nil {
					data = updated
				}
			}
		}
		return data

	default:
		data = r.restoreAtPath(data, "item")
		output := gjson.GetBytes(data, "response.output")
		if output.Exists() && output.IsArray() {
			for index := range output.Array() {
				data = r.restoreAtPath(data, fmt.Sprintf("response.output.%d", index))
			}
		}
		return data
	}
}

func (r *xaiNamespaceRestorer) restoreAtPath(data []byte, path string) []byte {
	if gjson.GetBytes(data, path+".type").String() != "function_call" {
		return data
	}
	qualifiedName := strings.TrimSpace(gjson.GetBytes(data, path+".name").String())
	ref, ok := r.refs[qualifiedName]
	if !ok {
		return data
	}
	if ref.isDispatcher {
		rawArgs := gjson.GetBytes(data, path+".arguments").String()
		childName, childArgs, unwrapped := unwrapXAIDispatcherArguments(rawArgs, ref.namespace, r.refs)
		if !unwrapped && childName == "" {
			childName = ref.name
		}
		updated, errSet := sjson.SetBytes(data, path+".namespace", ref.namespace)
		if errSet != nil {
			return data
		}
		if childName != "" {
			if updatedName, errSetName := sjson.SetBytes(updated, path+".name", childName); errSetName == nil {
				updated = updatedName
			}
		}
		if len(childArgs) > 0 {
			if updatedArgs, errSetArgs := sjson.SetBytes(updated, path+".arguments", string(childArgs)); errSetArgs == nil {
				updated = updatedArgs
			}
		}
		return updated
	}

	updated, errSet := sjson.SetBytes(data, path+".name", ref.name)
	if errSet != nil {
		return data
	}
	updated, errSet = sjson.SetBytes(updated, path+".namespace", ref.namespace)
	if errSet != nil {
		return data
	}
	return updated
}

func unwrapXAIDispatcherArguments(rawArgs string, namespaceName string, refs map[string]xaiNamespaceToolRef) (string, []byte, bool) {
	if !gjson.Valid(rawArgs) {
		return "", nil, false
	}
	argsParsed := gjson.Parse(rawArgs)
	nameField := argsParsed.Get("name")
	if !nameField.Exists() || nameField.Type != gjson.String {
		return "", nil, false
	}
	childName := strings.TrimSpace(nameField.String())
	if childName == "" {
		return "", nil, false
	}

	if namespaceName != "" {
		if dispRef, exists := refs[namespaceName]; exists && dispRef.isDispatcher && len(dispRef.children) > 0 {
			if _, isChild := dispRef.children[childName]; !isChild {
				return "", nil, false
			}
		} else {
			qualified := qualifyXAINamespaceToolName(namespaceName, childName)
			if ref, exists := refs[qualified]; exists && ref.isDispatcher {
				return "", nil, false
			}
		}
	} else {
		isChildOfDispatcher := false
		for _, ref := range refs {
			if ref.isDispatcher && (ref.name == childName || ref.namespace == childName) {
				isChildOfDispatcher = true
				break
			}
		}
		if !isChildOfDispatcher && !argsParsed.Get("arguments").Exists() {
			return "", nil, false
		}
	}

	var childArgs []byte
	if argsField := argsParsed.Get("arguments"); argsField.Exists() {
		if argsField.Type == gjson.String {
			childArgs = []byte(argsField.String())
		} else {
			childArgs = []byte(argsField.Raw)
		}
	} else {
		cleaned, errDel := sjson.DeleteBytes([]byte(rawArgs), "name")
		if errDel == nil && len(cleaned) > 0 && string(cleaned) != "{}" {
			childArgs = cleaned
		} else {
			childArgs = []byte("{}")
		}
	}
	if len(childArgs) == 0 {
		childArgs = []byte("{}")
	}
	return childName, childArgs, true
}

func restoreXAINamespaceToolCalls(data []byte, refs map[string]xaiNamespaceToolRef) []byte {
	restorer := newXAINamespaceRestorer(refs)
	return restorer.restore(data)
}

// restoreXAIClientWebSearchName rewrites the given alias
// back to the original client tool name "web_search" across event items and
// completed outputs. Only unnamespaced tool calls are restored so namespaced
// tools (e.g. acme.clientfn_web_search) are preserved untouched.
func restoreXAIClientWebSearchName(data []byte, alias string) []byte {
	if alias == "" || !bytes.Contains(data, []byte(alias)) {
		return data
	}
	if !gjson.ValidBytes(data) {
		return data
	}

	// Check item (e.g. response.output_item.added / response.output_item.done)
	if strings.TrimSpace(gjson.GetBytes(data, "item.namespace").String()) == "" {
		if strings.TrimSpace(gjson.GetBytes(data, "item.name").String()) == alias {
			data, _ = sjson.SetBytes(data, "item.name", xaiWebSearchToolType)
		}
		if strings.TrimSpace(gjson.GetBytes(data, "item.function.name").String()) == alias {
			data, _ = sjson.SetBytes(data, "item.function.name", xaiWebSearchToolType)
		}
	}

	// Check response.output array (e.g. response.completed / response.incomplete)
	respOutput := gjson.GetBytes(data, "response.output")
	if respOutput.IsArray() {
		for idx, item := range respOutput.Array() {
			if strings.TrimSpace(item.Get("namespace").String()) != "" {
				continue
			}
			if strings.TrimSpace(item.Get("name").String()) == alias {
				data, _ = sjson.SetBytes(data, fmt.Sprintf("response.output.%d.name", idx), xaiWebSearchToolType)
			}
			if strings.TrimSpace(item.Get("function.name").String()) == alias {
				data, _ = sjson.SetBytes(data, fmt.Sprintf("response.output.%d.function.name", idx), xaiWebSearchToolType)
			}
		}
	}

	// Check top-level output array (non-stream responses translated)
	topOutput := gjson.GetBytes(data, "output")
	if topOutput.IsArray() {
		for idx, item := range topOutput.Array() {
			if strings.TrimSpace(item.Get("namespace").String()) != "" {
				continue
			}
			if strings.TrimSpace(item.Get("name").String()) == alias {
				data, _ = sjson.SetBytes(data, fmt.Sprintf("output.%d.name", idx), xaiWebSearchToolType)
			}
			if strings.TrimSpace(item.Get("function.name").String()) == alias {
				data, _ = sjson.SetBytes(data, fmt.Sprintf("output.%d.function.name", idx), xaiWebSearchToolType)
			}
		}
	}

	// Check top-level name
	if strings.TrimSpace(gjson.GetBytes(data, "namespace").String()) == "" {
		if strings.TrimSpace(gjson.GetBytes(data, "name").String()) == alias {
			data, _ = sjson.SetBytes(data, "name", xaiWebSearchToolType)
		}
	}

	return data
}

// normalizeXAIObjectRootUnionBranchTypes makes untyped root union branches
// explicitly object-only when the parameter root already permits only objects.
// This preserves the original schema semantics while satisfying xAI validation.
func normalizeXAIObjectRootUnionBranchTypes(tool []byte) ([]byte, bool, bool) {
	parameters := gjson.GetBytes(tool, "parameters")
	rootType := parameters.Get("type")
	if rootType.Type != gjson.String || rootType.String() != "object" {
		return tool, false, true
	}

	original := tool
	changed := false
	for _, unionName := range []string{"anyOf", "oneOf"} {
		union := parameters.Get(unionName)
		if !union.IsArray() {
			continue
		}
		for index, branch := range union.Array() {
			if !branch.IsObject() || branch.Get("type").Exists() || branch.Get("$ref").Exists() {
				continue
			}
			updated, errSet := sjson.SetBytes(tool, fmt.Sprintf("parameters.%s.%d.type", unionName, index), "object")
			if errSet != nil {
				return original, false, false
			}
			tool = updated
			changed = true
		}
	}
	return tool, changed, true
}

func xaiSchemaTypeIsObjectOnly(schemaType gjson.Result) bool {
	if schemaType.Type == gjson.String {
		return strings.EqualFold(strings.TrimSpace(schemaType.String()), "object")
	}
	if !schemaType.IsArray() {
		return false
	}
	types := schemaType.Array()
	if len(types) == 0 {
		return false
	}
	for _, schemaTypeItem := range types {
		if schemaTypeItem.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(schemaTypeItem.String()), "object") {
			return false
		}
	}
	return true
}

func isXAICodexAppAutomationUpdate(toolName, namespaceName string) bool {
	cleanNamespace := strings.TrimPrefix(strings.TrimSpace(namespaceName), "mcp__")
	cleanTool := strings.TrimPrefix(strings.TrimSpace(toolName), "mcp__")
	if strings.EqualFold(cleanTool, xaiAutomationUpdateToolName) && (strings.EqualFold(cleanNamespace, xaiCodexAppNamespaceName) || strings.EqualFold(cleanNamespace, "codex_apps")) {
		return true
	}
	if strings.EqualFold(cleanTool, xaiCodexAppNamespaceName+"__"+xaiAutomationUpdateToolName) || strings.EqualFold(cleanTool, "codex_apps__"+xaiAutomationUpdateToolName) {
		return true
	}
	return false
}

// xaiFunctionParametersNeedSimplification reports whether a function tool, or
// a custom tool normalized to a function, has a schema that xAI cannot accept.
func xaiFunctionParametersNeedSimplification(tool gjson.Result, namespaceName string) bool {
	toolType := strings.TrimSpace(tool.Get("type").String())
	isFunction := strings.EqualFold(toolType, xaiFunctionToolType)
	isNormalizedCustom := strings.EqualFold(toolType, xaiCustomToolType)
	if !isFunction && !isNormalizedCustom {
		return false
	}

	toolName := strings.TrimSpace(tool.Get("name").String())
	if isFunction && isXAICodexAppAutomationUpdate(toolName, namespaceName) {
		return true
	}

	parameters := tool.Get("parameters")
	for _, unionName := range []string{"anyOf", "oneOf"} {
		union := parameters.Get(unionName)
		if !union.IsArray() {
			continue
		}
		for _, branch := range union.Array() {
			if branch.Get("$ref").Exists() || !xaiSchemaTypeIsObjectOnly(branch.Get("type")) {
				return true
			}
		}
	}
	return false
}

func sanitizeXAIInputEncryptedContent(body []byte) []byte {
	return sanitizeXAIInputEncryptedContentWithOptions(body, false)
}

func sanitizeXAIInputEncryptedContentForBaseURL(body []byte, baseURL string) []byte {
	return sanitizeXAIInputEncryptedContentWithOptions(body, isOpenRouterUpstreamURL(baseURL))
}

func sanitizeXAIInputEncryptedContentWithOptions(body []byte, allowRouterSuffix bool) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	changed := false
	dropCount := 0
	firstReason := ""
	firstItemType := ""
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType != "reasoning" && itemType != "compaction" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if !encryptedContent.Exists() {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		reason := ""
		rewrittenSig := ""
		switch encryptedContent.Type {
		case gjson.String:
			rawSig := encryptedContent.String()
			if info, err := signature.InspectGrokEncryptedContent(rawSig); err != nil {
				reason = err.Error()
			} else if info.RouterSuffix != "" && !allowRouterSuffix {
				rewrittenSig = stripRouterSuffix(rawSig, info.RouterSuffix)
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			if rewrittenSig != "" {
				if next, errSet := sjson.SetBytes([]byte(item.Raw), "encrypted_content", rewrittenSig); errSet == nil {
					items = append(items, json.RawMessage(next))
					changed = true
					continue
				}
			}
			items = append(items, json.RawMessage(item.Raw))
			continue
		}

		if itemType == "compaction" {
			changed = true
			dropCount++
			if firstReason == "" {
				firstReason = reason
				firstItemType = itemType
			}
			continue
		}

		next, err := sjson.DeleteBytes([]byte(item.Raw), "encrypted_content")
		if err != nil {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(next))
		changed = true
		dropCount++
		if firstReason == "" {
			firstReason = reason
			firstItemType = itemType
		}
	}
	if !changed {
		return body
	}
	rawInput, err := json.Marshal(items)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "input", rawInput)
	if err != nil {
		return body
	}
	if dropCount > 0 {
		log.WithFields(log.Fields{
			"component":       "xai_encrypted_content_sanitizer",
			"dropped":         dropCount,
			"first_item_type": firstItemType,
			"first_reason":    firstReason,
		}).Debug("xai executor: removed invalid encrypted_content before upstream")
	}
	return mergeAdjacentXAIInputReasoningSummaries(updated)
}

func normalizeXAIInputReasoningItems(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	updated := body
	for i, item := range input.Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		contentPath := fmt.Sprintf("input.%d.content", i)
		if content := gjson.GetBytes(updated, contentPath); content.Exists() && content.Type == gjson.Null {
			updatedBody, errDel := sjson.DeleteBytes(updated, contentPath)
			if errDel != nil {
				return body
			}
			updated = updatedBody
		}
		encryptedContentPath := fmt.Sprintf("input.%d.encrypted_content", i)
		if encryptedContent := gjson.GetBytes(updated, encryptedContentPath); encryptedContent.Exists() && encryptedContent.Type == gjson.Null {
			updatedBody, errDel := sjson.DeleteBytes(updated, encryptedContentPath)
			if errDel != nil {
				return body
			}
			updated = updatedBody
		}
	}
	return mergeAdjacentXAIInputReasoningSummaries(updated)
}

func mergeAdjacentXAIInputReasoningSummaries(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	changed := false
	items := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		if len(items) > 0 && canMergeXAIReasoningSummary(items[len(items)-1], item) {
			merged, ok := appendXAIReasoningSummary(items[len(items)-1], item.Get("summary").Array())
			if ok {
				items[len(items)-1] = json.RawMessage(merged)
				changed = true
				continue
			}
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return body
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

func canMergeXAIReasoningSummary(previous json.RawMessage, current gjson.Result) bool {
	previousItem := gjson.ParseBytes(previous)
	if previousItem.Get("type").String() != "reasoning" || current.Get("type").String() != "reasoning" {
		return false
	}
	if !previousItem.Get("summary").IsArray() || !current.Get("summary").IsArray() {
		return false
	}
	if len(current.Get("summary").Array()) == 0 {
		return false
	}
	for name := range current.Map() {
		if name != "type" && name != "summary" {
			return false
		}
	}
	return true
}

func appendXAIReasoningSummary(previous json.RawMessage, currentSummary []gjson.Result) ([]byte, bool) {
	updated := []byte(previous)
	summary := gjson.GetBytes(updated, "summary")
	if !summary.IsArray() {
		return previous, false
	}
	nextIndex := len(summary.Array())
	for i, item := range currentSummary {
		updatedItem, errSet := sjson.SetRawBytes(updated, fmt.Sprintf("summary.%d", nextIndex+i), []byte(item.Raw))
		if errSet != nil {
			return previous, false
		}
		updated = updatedItem
	}
	return updated, true
}

func xaiNormalizeReasoningSummaryEventLine(line []byte, eventName string) []byte {
	if eventName == "" && bytes.HasPrefix(line, xaiEventTag) {
		eventName = strings.TrimSpace(string(line[len(xaiEventTag):]))
	}
	eventName = xaiNormalizeReasoningSummaryEventName(eventName)
	if eventName == "" {
		return bytes.Clone(line)
	}
	return []byte("event: " + eventName)
}

func xaiNormalizeReasoningSummaryEventName(eventName string) string {
	switch eventName {
	case "response.reasoning_text.delta":
		return "response.reasoning_summary_text.delta"
	case "response.reasoning_text.done":
		return "response.reasoning_summary_part.done"
	default:
		return eventName
	}
}

func xaiNormalizeReasoningSummaryData(eventData []byte) []byte {
	if len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return eventData
	}

	normalized := eventData
	switch gjson.GetBytes(normalized, "type").String() {
	case "response.reasoning_text.delta":
		normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_text.delta")
		normalized = xaiNormalizeReasoningSummaryIndex(normalized)
	case "response.reasoning_text.done":
		normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.done")
		normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
		if text := gjson.GetBytes(normalized, "text"); text.Exists() {
			normalized, _ = sjson.SetBytes(normalized, "part.text", text.String())
		}
		normalized, _ = sjson.DeleteBytes(normalized, "text")
		normalized = xaiNormalizeReasoningSummaryIndex(normalized)
	case "response.content_part.added":
		if gjson.GetBytes(normalized, "part.type").String() == "reasoning_text" {
			normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.added")
			normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
			normalized = xaiNormalizeReasoningSummaryIndex(normalized)
		}
	case "response.content_part.done":
		if gjson.GetBytes(normalized, "part.type").String() == "reasoning_text" {
			normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.done")
			normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
			normalized = xaiNormalizeReasoningSummaryIndex(normalized)
		}
	}

	if item := gjson.GetBytes(normalized, "item"); item.Exists() && item.Type == gjson.JSON {
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			normalized, _ = sjson.SetRawBytes(normalized, "item", updatedItem)
		}
	}
	if output := gjson.GetBytes(normalized, "response.output"); output.IsArray() {
		updatedOutput, changed := xaiNormalizeReasoningOutputItems(output.Array())
		if changed {
			normalized, _ = sjson.SetRawBytes(normalized, "response.output", updatedOutput)
		}
	}

	return normalized
}

func xaiNormalizeReasoningSummaryDataEvents(eventData []byte) [][]byte {
	if len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return [][]byte{eventData}
	}
	if gjson.GetBytes(eventData, "type").String() != "response.reasoning_text.done" {
		return [][]byte{xaiNormalizeReasoningSummaryData(eventData)}
	}

	textDone, _ := sjson.SetBytes(eventData, "type", "response.reasoning_summary_text.done")
	textDone = xaiNormalizeReasoningSummaryIndex(textDone)
	partDone := xaiNormalizeReasoningSummaryData(eventData)
	return [][]byte{textDone, partDone}
}

func xaiNormalizeReasoningSummaryIndex(eventData []byte) []byte {
	contentIndex := gjson.GetBytes(eventData, "content_index")
	if contentIndex.Exists() && contentIndex.Raw != "" && !gjson.GetBytes(eventData, "summary_index").Exists() {
		eventData, _ = sjson.SetRawBytes(eventData, "summary_index", []byte(contentIndex.Raw))
	}
	eventData, _ = sjson.DeleteBytes(eventData, "content_index")
	return eventData
}

func xaiNormalizeReasoningOutputItems(items []gjson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			changed = true
		}
		buf.Write(updatedItem)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiNormalizeReasoningOutputItem(item []byte) []byte {
	if !gjson.ValidBytes(item) || gjson.GetBytes(item, "type").String() != "reasoning" {
		return item
	}

	normalized := item
	if summary := gjson.GetBytes(normalized, "summary"); summary.IsArray() {
		updatedSummary, changed := xaiNormalizeReasoningSummaryItems(summary.Array())
		if changed {
			normalized, _ = sjson.SetRawBytes(normalized, "summary", updatedSummary)
		}
	}

	content := gjson.GetBytes(normalized, "content")
	if !content.IsArray() {
		return normalized
	}

	summaryItems := make([]gjson.Result, 0, len(content.Array()))
	for _, part := range content.Array() {
		if part.Get("type").String() == "reasoning_text" {
			summaryItems = append(summaryItems, part)
		}
	}
	if len(summaryItems) == 0 {
		return normalized
	}

	updatedSummary, _ := xaiNormalizeReasoningSummaryItems(summaryItems)
	normalized, _ = sjson.SetRawBytes(normalized, "summary", updatedSummary)
	normalized, _ = sjson.DeleteBytes(normalized, "content")
	return normalized
}

func xaiNormalizeReasoningSummaryItems(items []gjson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		itemRaw := []byte(item.Raw)
		if item.Get("type").String() == "reasoning_text" {
			var errSet error
			itemRaw, errSet = sjson.SetBytes(itemRaw, "type", "summary_text")
			if errSet == nil {
				changed = true
			}
		}
		buf.Write(itemRaw)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiCollectOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func xaiPatchCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	eventData = helps.EnsureResponsesUsageDetails(eventData)
	outputResult := gjson.GetBytes(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	outputArray := []byte("[]")
	var buf bytes.Buffer
	buf.WriteByte('[')
	wrote := false
	for _, idx := range indexes {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(outputItemsByIndex[idx])
		wrote = true
	}
	for _, item := range outputItemsFallback {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(item)
		wrote = true
	}
	buf.WriteByte(']')
	if wrote {
		outputArray = buf.Bytes()
	}

	patched, _ := sjson.SetRawBytes(eventData, "response.output", outputArray)
	return patched
}

// xaiFreeUsageExhaustedCooldown is the free-tier rolling window advertised by
// cli-chat-proxy ("Usage resets over a rolling 24-hour window").
const xaiFreeUsageExhaustedCooldown = 24 * time.Hour

const (
	xaiEmptyStreamMessage      = "xai stream error: empty upstream stream"
	xaiIncompleteStreamMessage = "xai stream error: stream closed before response.completed or response.incomplete"
)

type xaiRequestScopedStreamError struct {
	statusErr
}

func (xaiRequestScopedStreamError) IsRequestScoped() bool {
	return true
}

func newXAIEmptyStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiEmptyStreamMessage,
	}}
}

func newXAIIncompleteStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiIncompleteStreamMessage,
	}}
}

// xaiJSONDoc is a small mutable JSON document over gjson/sjson used to build
// normalized terminal error bodies.
type xaiJSONDoc struct {
	raw []byte
}

func newXAIJSONDoc(raw string) *xaiJSONDoc {
	return &xaiJSONDoc{raw: []byte(raw)}
}

func (d *xaiJSONDoc) Get(path string) gjson.Result {
	return gjson.GetBytes(d.raw, path)
}

func (d *xaiJSONDoc) Set(path string, value any) error {
	updated, errSet := sjson.SetBytes(d.raw, path, value)
	if errSet != nil {
		return errSet
	}
	d.raw = updated
	return nil
}

func (d *xaiJSONDoc) SetRawBytes(path string, value []byte) error {
	updated, errSet := sjson.SetRawBytes(d.raw, path, value)
	if errSet != nil {
		return errSet
	}
	d.raw = updated
	return nil
}

func (d *xaiJSONDoc) Bytes() []byte {
	return d.raw
}

func xaiTerminalFailureErr(eventName string, eventData []byte) (statusErr, bool) {
	doc, ok := xaiTerminalFailureDoc(eventName, eventData)
	if !ok || doc == nil {
		return statusErr{}, false
	}
	status := xaiTerminalFailureStatus(eventData, doc)
	return xaiStatusErr(status, doc.Bytes()), true
}

func xaiTerminalFailureDoc(eventName string, eventData []byte) (*xaiJSONDoc, bool) {
	trimmed := bytes.TrimSpace(eventData)
	if len(trimmed) == 0 {
		return nil, false
	}
	if !gjson.ValidBytes(trimmed) {
		if strings.EqualFold(strings.TrimSpace(eventName), "error") {
			doc := newXAIJSONDoc(`{"error":{}}`)
			_ = doc.Set("error.message", string(trimmed))
			return doc, true
		}
		return nil, false
	}

	eventType := strings.TrimSpace(gjson.GetBytes(trimmed, "type").String())
	if eventType == "" {
		normalizedEventName := strings.TrimSpace(eventName)
		if normalizedEventName == "error" || normalizedEventName == "response.failed" {
			eventType = normalizedEventName
		} else if errField := gjson.GetBytes(trimmed, "error"); errField.Exists() && errField.Type != gjson.Null && !gjson.GetBytes(trimmed, "output").Exists() {
			if errField.Type == gjson.JSON || strings.TrimSpace(errField.String()) != "" {
				eventType = "error"
			}
		}
	}

	var doc *xaiJSONDoc
	switch eventType {
	case "error":
		doc = xaiTerminalErrorDoc(trimmed, "error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "body.error")
		}
		if doc == nil {
			doc = xaiTerminalTopLevelErrorDoc(trimmed)
		}
	case "response.failed":
		doc = xaiTerminalErrorDoc(trimmed, "response.error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "error")
		}
	default:
		return nil, false
	}

	if doc == nil {
		doc = newXAIJSONDoc(`{"error":{"message":"upstream stream failed without error details"}}`)
	}
	if seq := gjson.GetBytes(trimmed, "sequence_number"); seq.Exists() && seq.Type == gjson.Number {
		_ = doc.Set("sequence_number", seq.Int())
	}
	return doc, true
}

func xaiTerminalErrorDoc(eventData []byte, path string) *xaiJSONDoc {
	errorResult := gjson.GetBytes(eventData, path)
	if !errorResult.Exists() || errorResult.Type == gjson.Null {
		return nil
	}

	doc := newXAIJSONDoc(`{"error":{}}`)
	hasField := false
	if errorResult.Type == gjson.JSON {
		if errorResult.IsObject() && len(errorResult.Map()) > 0 {
			_ = doc.SetRawBytes("error", []byte(errorResult.Raw))
			hasField = true
		}
	} else if message := strings.TrimSpace(errorResult.String()); message != "" {
		_ = doc.Set("error.message", message)
		hasField = true
	}

	if strings.TrimSpace(doc.Get("error.code").String()) == "" {
		for _, codePath := range []string{"response.error.code", "code", "body.error.code"} {
			if code := strings.TrimSpace(gjson.GetBytes(eventData, codePath).String()); code != "" {
				_ = doc.Set("error.code", code)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.type").String()) == "" {
		for _, typePath := range []string{"response.error.type", "error_type", "body.error.type"} {
			if errType := strings.TrimSpace(gjson.GetBytes(eventData, typePath).String()); errType != "" {
				_ = doc.Set("error.type", errType)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.param").String()) == "" {
		for _, paramPath := range []string{"response.error.param", "param", "body.error.param"} {
			if param := strings.TrimSpace(gjson.GetBytes(eventData, paramPath).String()); param != "" {
				_ = doc.Set("error.param", param)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		for _, msgPath := range []string{"response.error.message", "message", "body.error.message"} {
			if message := strings.TrimSpace(gjson.GetBytes(eventData, msgPath).String()); message != "" {
				_ = doc.Set("error.message", message)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		if code := strings.TrimSpace(doc.Get("error.code").String()); code != "" {
			_ = doc.Set("error.message", code)
			hasField = true
		} else if errType := strings.TrimSpace(doc.Get("error.type").String()); errType != "" {
			_ = doc.Set("error.message", errType)
			hasField = true
		}
	}
	if !hasField {
		return nil
	}
	return doc
}

func xaiTerminalTopLevelErrorDoc(eventData []byte) *xaiJSONDoc {
	message := strings.TrimSpace(gjson.GetBytes(eventData, "message").String())
	code := strings.TrimSpace(gjson.GetBytes(eventData, "code").String())
	errorType := strings.TrimSpace(gjson.GetBytes(eventData, "error_type").String())
	param := strings.TrimSpace(gjson.GetBytes(eventData, "param").String())
	if message == "" && code == "" && errorType == "" && param == "" {
		return nil
	}

	doc := newXAIJSONDoc(`{"error":{}}`)
	if message != "" {
		_ = doc.Set("error.message", message)
	}
	if code != "" {
		_ = doc.Set("error.code", code)
	}
	if errorType != "" {
		_ = doc.Set("error.type", errorType)
	}
	if param != "" {
		_ = doc.Set("error.param", param)
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		if code != "" {
			_ = doc.Set("error.message", code)
		} else if errorType != "" {
			_ = doc.Set("error.message", errorType)
		}
	}
	return doc
}

func xaiTerminalFailureStatus(eventData []byte, doc *xaiJSONDoc) int {
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code", "body.status", "body.error.status"} {
		res := gjson.GetBytes(eventData, path)
		switch res.Type {
		case gjson.Number:
			if status := int(res.Int()); status >= 400 && status <= 599 {
				return status
			}
		case gjson.String:
			if status, errAtoi := strconv.Atoi(strings.TrimSpace(res.String())); errAtoi == nil && status >= 400 && status <= 599 {
				return status
			}
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(doc.Get("error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(doc.Get("error.code").String()))
	errorMsg := strings.ToLower(strings.TrimSpace(doc.Get("error.message").String()))
	if status, errAtoi := strconv.Atoi(errorCode); errAtoi == nil && status >= 400 && status <= 599 {
		return status
	}

	switch {
	case errorType == "authentication_error",
		errorCode == "invalid_api_key",
		errorCode == "unauthorized",
		strings.HasPrefix(errorCode, "unauthenticated"),
		strings.Contains(errorCode, "bad-credentials"),
		strings.Contains(errorMsg, "access token could not be validated"):
		return http.StatusUnauthorized
	case errorType == "permission_error",
		errorCode == "forbidden",
		errorCode == "permission_denied",
		errorCode == "permission-denied",
		strings.HasPrefix(errorCode, "permission_denied:"),
		strings.HasPrefix(errorCode, "permission-denied:"):
		return http.StatusForbidden
	case errorType == "not_found_error",
		errorCode == "not_found",
		errorCode == "model_not_found":
		return http.StatusNotFound
	case errorType == "rate_limit_error",
		errorCode == "rate_limit_exceeded",
		errorCode == "rate_limit",
		strings.Contains(errorCode, "free-usage-exhausted"),
		strings.Contains(errorCode, "resource_exhausted"),
		strings.Contains(errorCode, "resource-exhausted"),
		strings.Contains(errorMsg, "free-usage-exhausted"),
		strings.Contains(errorMsg, "included free usage"):
		return http.StatusTooManyRequests
	case errorCode == "cyber_policy",
		errorCode == "cyber_policy_violation",
		errorCode == "bio_policy",
		errorCode == "misalignment_policy_violation",
		errorCode == "invalid_prompt",
		errorCode == "content_policy_violation",
		errorCode == "image_content_policy_violation",
		errorType == "invalid_request_error",
		errorType == "invalid_request",
		errorType == "bad_request_error",
		errorType == "image_generation_user_error",
		errorCode == "invalid_value",
		errorCode == "invalid_request_error",
		errorCode == "invalid_argument",
		errorCode == "invalid-argument",
		errorCode == "context_length_exceeded",
		errorCode == "context_too_large":
		return http.StatusBadRequest
	case errorType == "service_unavailable_error",
		errorCode == "server_is_overloaded",
		errorCode == "service_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// xaiStatusErr normalizes upstream xAI error bodies for conductor behavior:
//   - credential invalidation (403 bad-credentials) is remapped to 401 so the
//     existing OAuth refresh-once-and-retry path runs instead of payment cooldown
//   - free-tier exhaustion (subscription:free-usage-exhausted) carries a 24h
//     RetryAfter hint for auth cooldown / account rotation
//
// Generic 429s stay without an explicit retry hint so conductor backoff still applies.
func xaiStatusErr(code int, body []byte) statusErr {
	err := statusErr{code: code, msg: string(body)}
	if len(body) == 0 {
		return err
	}
	if code == http.StatusForbidden && isXAIBadCredentialsBody(body) {
		// Upstream returns 403 for invalidated OAuth access tokens. Map to 401 so
		// tryRefreshAfterUnauthorized / MarkResult unauthorized handling applies.
		err.code = http.StatusUnauthorized
		return err
	}
	if code != http.StatusTooManyRequests {
		return err
	}
	codeStr := strings.ToLower(gjson.GetBytes(body, "code").String())
	if codeStr == "" {
		codeStr = strings.ToLower(gjson.GetBytes(body, "error.code").String())
	}
	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	if msg == "" {
		msg = strings.ToLower(gjson.GetBytes(body, "error").String())
	}
	if msg == "" {
		msg = strings.ToLower(string(body))
	}
	if strings.Contains(codeStr, "free-usage-exhausted") ||
		strings.Contains(msg, "free-usage-exhausted") ||
		strings.Contains(msg, "included free usage") {
		d := xaiFreeUsageExhaustedCooldown
		err.retryAfter = &d
	}
	return err
}

// isXAIBadCredentialsBody reports whether an xAI error body indicates an
// invalidated/unusable OAuth access token rather than a generic permission or
// payment failure. HTTP and websocket payloads both use this helper, so nested
// error.code / error.message shapes are checked as well as flat bodies.
func isXAIBadCredentialsBody(body []byte) bool {
	for _, path := range []string{"code", "error.code", "body.error.code"} {
		if strings.Contains(strings.ToLower(gjson.GetBytes(body, path).String()), "bad-credentials") {
			return true
		}
	}
	for _, path := range []string{"error", "error.message", "message", "body.error", "body.error.message"} {
		msg := strings.ToLower(gjson.GetBytes(body, path).String())
		if strings.Contains(msg, "access token could not be validated") {
			return true
		}
	}
	raw := strings.ToLower(string(body))
	return strings.Contains(raw, "bad-credentials") ||
		strings.Contains(raw, "access token could not be validated")
}
