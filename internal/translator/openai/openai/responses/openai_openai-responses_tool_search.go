package responses

import (
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Chat Completions has no tool search, so the client-executed Responses
// tool_search declaration is bridged as an ordinary function. Calls to it are
// restored as tool_search_call items, the client runs the search, and the
// tools it returns in tool_search_output stay declared as functions on every
// later request (see walkResponsesToolDeclarations).

const (
	responsesToolSearchIDPrefix          = "tsc_"
	responsesToolSearchDefaultParameters = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
)

func convertResponsesToolSearchToolToOpenAIChat(tool gjson.Result, name string) ([]byte, bool) {
	chatTool := []byte(`{"type":"function","function":{"name":"","description":"Search for deferred tools and load the matching tools.","parameters":{}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := util.ResponsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	parameters := responsesToolSearchDefaultParameters
	if declared := util.ResponsesToolParameters(tool); declared.Exists() && declared.IsObject() {
		parameters = declared.Raw
	}
	chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", []byte(parameters))
	return chatTool, true
}

func (idx *responsesToolIndex) isToolSearch(name string) bool {
	return idx.byChat[name].search
}

func (idx *responsesToolIndex) toolSearchName() string {
	for _, d := range idx.declarations {
		if d.search {
			return d.chatName
		}
	}
	return ""
}

// toolSearchHistory rewrites replayed client tool_search_call and
// tool_search_output items into the function_call pair that the Chat
// Completions conversion already groups and pairs. Hosted (server) search
// items are left as they are.
func (idx *responsesToolIndex) toolSearchHistory(items []gjson.Result) []gjson.Result {
	var out []gjson.Result
	name := ""
	resolveName := func() string {
		if name != "" {
			return name
		}
		name = idx.toolSearchName()
		if name != "" {
			return name
		}
		reserved := make(map[string]bool)
		for key := range idx.byChat {
			reserved[key] = true
		}
		for key := range idx.byRaw {
			reserved[key] = true
		}
		for key := range idx.byLocal {
			reserved[key] = true
		}
		for _, item := range items {
			if kind := item.Get("type").String(); kind == "function_call" || kind == "custom_tool_call" {
				reserved[idx.canonicalName(item.Get("name").String())] = true
			}
		}
		name = util.ResponsesToolSearchFunctionName
		for suffix := 1; reserved[name]; suffix++ {
			name = util.ResponsesToolSearchFunctionName + "_" + strconv.Itoa(suffix)
		}
		return name
	}
	for i, item := range items {
		itemType := item.Get("type").String()
		if (itemType != util.ResponsesToolSearchCallType && itemType != util.ResponsesToolSearchOutputType) ||
			!util.IsResponsesClientToolSearchItem(item) {
			if out != nil {
				out = append(out, item)
			}
			continue
		}
		if out == nil {
			out = append(make([]gjson.Result, 0, len(items)), items[:i]...)
		}
		if itemType == util.ResponsesToolSearchCallType {
			out = append(out, idx.toolSearchCallAsFunctionCall(item, resolveName()))
		} else {
			out = append(out, idx.toolSearchOutputAsFunctionCallOutput(item))
		}
	}
	if out == nil {
		return items
	}
	return out
}

func (idx *responsesToolIndex) toolSearchCallAsFunctionCall(item gjson.Result, searchName string) gjson.Result {
	callJSON := []byte(`{"type":"function_call","call_id":"","name":"","arguments":"{}"}`)
	callJSON, _ = sjson.SetBytes(callJSON, "call_id", translatorcommon.ExtractResponsesCallID(item))
	callJSON, _ = sjson.SetBytes(callJSON, "name", searchName)
	if arguments := item.Get("arguments"); arguments.Type == gjson.String && strings.TrimSpace(arguments.String()) != "" {
		callJSON, _ = translatorcommon.SetStringWithoutHTMLEscape(callJSON, "arguments", arguments.String())
	} else if arguments.IsObject() {
		callJSON, _ = translatorcommon.SetStringWithoutHTMLEscape(callJSON, "arguments", arguments.Raw)
	}
	return util.ParseGJSONBytesNoCopy(callJSON)
}

func (idx *responsesToolIndex) toolSearchOutputAsFunctionCallOutput(item gjson.Result) gjson.Result {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	appendName := func(name string) {
		if _, exists := seen[name]; name == "" || exists {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	item.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			tool.Get("tools").ForEach(func(_, child gjson.Result) bool {
				if childName := responsesToolName(child); childName != "" {
					appendName(idx.namespaceName(namespaceName, childName))
				}
				return true
			})
		} else if name := responsesToolName(tool); name != "" {
			appendName(idx.canonicalName(name))
		}
		return true
	})

	summary := []byte(`{"status":"completed","tools":[]}`)
	if status := strings.TrimSpace(item.Get("status").String()); status != "" {
		summary, _ = sjson.SetBytes(summary, "status", status)
	}
	summary, _ = sjson.SetBytes(summary, "tools", names)
	outputJSON := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
	outputJSON, _ = sjson.SetBytes(outputJSON, "call_id", translatorcommon.ExtractResponsesCallID(item))
	outputJSON, _ = translatorcommon.SetStringWithoutHTMLEscape(outputJSON, "output", string(summary))
	return util.ParseGJSONBytesNoCopy(outputJSON)
}

// toolSearchCallItem renders a bridged search function call as a
// client-executed Responses tool_search_call with object arguments.
func toolSearchCallItem(callID, args, status string) []byte {
	trimmed := strings.TrimSpace(args)
	parsedArgs := gjson.Parse(trimmed)
	validArgs := gjson.Valid(trimmed) && parsedArgs.IsObject()
	if status == "completed" && !validArgs {
		status = "incomplete"
	}
	itemJSON := []byte(`{"id":"","type":"tool_search_call","status":"","execution":"client","call_id":"","arguments":{}}`)
	itemJSON, _ = sjson.SetBytes(itemJSON, "id", responsesToolSearchIDPrefix+callID)
	itemJSON, _ = sjson.SetBytes(itemJSON, "status", status)
	itemJSON, _ = sjson.SetBytes(itemJSON, "call_id", callID)
	if validArgs {
		itemJSON, _ = sjson.SetRawBytes(itemJSON, "arguments", []byte(parsedArgs.Raw))
	}
	return itemJSON
}

func toolSearchCallPlaceholder(callID string) []byte {
	itemJSON := []byte(`{"id":"","type":"tool_search_call","status":"in_progress","execution":"client","call_id":"","arguments":{}}`)
	itemJSON, _ = sjson.SetBytes(itemJSON, "id", responsesToolSearchIDPrefix+callID)
	itemJSON, _ = sjson.SetBytes(itemJSON, "call_id", callID)
	return itemJSON
}
