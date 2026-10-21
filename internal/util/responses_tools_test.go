package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCollectResponsesToolDescriptors_PriorityAndNamespace(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "top_fn", "description": "top function"}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "namespace",
						"name": "ns1",
						"tools": [
							{"type": "function", "name": "child_fn", "description": "child function"},
							{"type": "custom", "name": "child_custom", "description": "child custom"}
						]
					},
					{"type": "custom", "name": "direct_custom"}
				]
			}
		]
	}`

	root := gjson.Parse(raw)
	descriptors := CollectResponsesToolDescriptors(root)
	if len(descriptors) != 4 {
		t.Fatalf("expected 4 descriptors, got %d", len(descriptors))
	}

	decls, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(root)
	if len(decls) != 4 {
		t.Fatalf("expected 4 declarations, got %d", len(decls))
	}

	if forwardMap["ns1__child_fn"] != "ns1__child_fn" {
		t.Fatalf("forwardMap['ns1__child_fn'] = %q, want ns1__child_fn", forwardMap["ns1__child_fn"])
	}

	childCustomIdentity := reverseMap["ns1__child_custom"]
	if childCustomIdentity.Name != "child_custom" || childCustomIdentity.Namespace != "ns1" || !childCustomIdentity.Custom {
		t.Fatalf("unexpected reverseMap for ns1__child_custom: %+v", childCustomIdentity)
	}

	topFnIdentity := reverseMap["top_fn"]
	if topFnIdentity.Name != "top_fn" || topFnIdentity.Namespace != "" || topFnIdentity.Custom {
		t.Fatalf("unexpected reverseMap for top_fn: %+v", topFnIdentity)
	}
}

func TestResponsesToolWinners_TopLevelBeatsAdditionalTools(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "shared_fn", "description": "top level"}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{"type": "function", "name": "shared_fn", "description": "additional"}
				]
			}
		]
	}`

	root := gjson.Parse(raw)
	winners := CollectResponsesToolWinners(root)
	winner := winners["shared_fn"]
	if winner.SourcePriority != 0 {
		t.Fatalf("winner priority = %d, want 0", winner.SourcePriority)
	}
	if winner.Tool.Get("description").String() != "top level" {
		t.Fatalf("winner description = %q, want 'top level'", winner.Tool.Get("description").String())
	}
}

func TestResponsesToolWinners_DirectBeatsNamespaceChild(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "namespace", "name": "n", "tools": [{"type": "function", "name": "x", "description": "namespace child"}]},
			{"type": "custom", "name": "n__x", "description": "direct"}
		]
	}`

	root := gjson.Parse(raw)
	winners := CollectResponsesToolWinners(root)
	winner := winners["n__x"]
	if !winner.Direct {
		t.Fatalf("winner direct = %v, want true", winner.Direct)
	}
	if winner.ToolType != "custom" {
		t.Fatalf("winner toolType = %q, want custom", winner.ToolType)
	}
}

func TestUnwrapResponsesCustomToolInput(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `{"input":"pwd"}`, want: "pwd"},
		{input: `{"input":{"cmd":"ls"}}`, want: `{"cmd":"ls"}`},
		{input: `"direct text"`, want: "direct text"},
		{input: `{}`, want: ""},
		{input: ``, want: ""},
	}

	for _, tt := range tests {
		got := UnwrapResponsesCustomToolInput(tt.input)
		if got != tt.want {
			t.Errorf("UnwrapResponsesCustomToolInput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildGeminiFunctionDeclarations_DisambiguationAndLongNames(t *testing.T) {
	// Two tools that genuinely collide after sanitization (e.g. "read/file" vs "read_file"), and one > 64 chars
	raw := `{
		"tools": [
			{"type": "function", "name": "read/file", "description": "tool with slash"},
			{"type": "function", "name": "read_file", "description": "tool with underscore"},
			{"type": "custom", "name": "mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars"}
		]
	}`

	root := gjson.Parse(raw)
	decls, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(root)
	if len(decls) != 3 {
		t.Fatalf("expected 3 decls, got %d", len(decls))
	}

	name1 := forwardMap["read/file"]
	name2 := forwardMap["read_file"]
	if name1 == name2 {
		t.Fatalf("colliding tools mapped to identical name: %q", name1)
	}

	identity1 := reverseMap[name1]
	if identity1.Name != "read/file" {
		t.Fatalf("reverseMap[%q].Name = %q, want read/file", name1, identity1.Name)
	}
	identity2 := reverseMap[name2]
	if identity2.Name != "read_file" {
		t.Fatalf("reverseMap[%q].Name = %q, want read_file", name2, identity2.Name)
	}

	longName := forwardMap["mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars"]
	if len(longName) > 64 {
		t.Fatalf("long tool name length = %d > 64: %q", len(longName), longName)
	}

	identityLong := reverseMap[longName]
	if !identityLong.Custom || identityLong.Name != "mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars" {
		t.Fatalf("unexpected reverse identity for long name: %+v", identityLong)
	}
}

func TestBuildGeminiApplyPatchDeclaration(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`)
	declarations, _, reverse := BuildGeminiFunctionDeclarations(root)
	if len(declarations) != 1 {
		t.Fatalf("declarations: %d", len(declarations))
	}
	declaration := gjson.ParseBytes(declarations[0])
	if !strings.Contains(declaration.Get("description").String(), "*** Begin Patch") || !strings.Contains(declaration.Get("description").String(), "start: patch") {
		t.Fatal("patch format is missing")
	}
	if declaration.Get("parametersJsonSchema.properties.input.type").String() != "string" || !declaration.Get("parametersJsonSchema.additionalProperties").Exists() || declaration.Get("parametersJsonSchema.additionalProperties").Bool() {
		t.Fatal("input schema is missing")
	}
	if !reverse[declaration.Get("name").String()].Custom {
		t.Fatal("custom client identity was lost")
	}
}

func TestBuildGeminiApplyPatchIdentityUsesWinningOriginalDeclaration(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"namespace","name":"a.b","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"a_b__apply_patch","parameters":{"type":"object","properties":{"n":{"type":"number"}}}},{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	declarations, forward, reverse := BuildGeminiFunctionDeclarations(root)
	if len(declarations) != 3 {
		t.Fatalf("declarations=%d", len(declarations))
	}
	if forward["a.b__apply_patch"] == forward["a_b__apply_patch"] {
		t.Fatal("sanitized collision was not separated")
	}
	for _, raw := range declarations {
		decl := gjson.ParseBytes(raw)
		identity := reverse[decl.Get("name").String()]
		patch := identity.Custom && identity.Namespace == "a.b"
		if identity.ApplyPatch != patch || strings.Contains(decl.Get("description").String(), "*** Begin Patch") != patch {
			t.Fatalf("wrong winning identity=%+v declaration=%s", identity, raw)
		}
	}
	if reverse[forward["apply_patch"]].Custom || reverse[forward["apply_patch"]].ApplyPatch {
		t.Fatal("ordinary function became custom patch")
	}
}

func TestBuildGeminiFunctionDeclarations_ClientToolSearchShimAndDeferredFiltering(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "exec_command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}},
			{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["query"], "additionalProperties": false}},
			{"type": "function", "name": "deferred_fn", "defer_loading": true, "parameters": {"type": "object", "properties": {}}},
			{"type": "web_search"}
		]
	}`
	declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))

	if len(declarations) != 2 {
		t.Fatalf("expected exec_command and tool_search declarations (with deferred_fn hidden), got %d: %s", len(declarations), declarations)
	}
	shim := gjson.ParseBytes(declarations[1])
	if got := shim.Get("name").String(); got != "tool_search" {
		t.Fatalf("shim name = %q, want tool_search", got)
	}
	if got := shim.Get("description").String(); got != "Search deferred tools." {
		t.Fatalf("shim description = %q, want original description", got)
	}
	if got := shim.Get("parametersJsonSchema.required.0").String(); got != "query" {
		t.Fatalf("shim parameters not preserved: %s", shim.Raw)
	}
	if got := forwardMap["tool_search"]; got != "tool_search" {
		t.Fatalf("forward map for shim = %q, want tool_search", got)
	}
	if _, ok := reverseMap["tool_search"]; !ok {
		t.Fatalf("expected tool_search in reverseMap")
	}
	if !reverseMap["tool_search"].ToolSearch || reverseMap["tool_search"].Custom || reverseMap["tool_search"].Namespace != "" {
		t.Fatalf("unexpected shim identity: %+v", reverseMap["tool_search"])
	}
	if reverseMap["exec_command"].ToolSearch {
		t.Fatalf("regular function must not be marked ToolSearch: %+v", reverseMap["exec_command"])
	}
}

func TestBuildGeminiFunctionDeclarations_CollectsToolSearchOutputTools(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "namespace", "name": "mcp__cua_repl", "tools": [{"type": "function", "name": "js", "description": "current"}]},
			{"type": "tool_search", "execution": "client"},
			{"type": "function", "name": "deferred_fn", "defer_loading": true, "description": "top-level deferred"}
		],
		"input": [
			{"type": "tool_search_call", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture"}},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "description": "fixture", "tools": [
					{"type": "function", "name": "ping", "defer_loading": true, "parameters": {"type": "object", "properties": {}, "additionalProperties": false}}
				]},
				{"type": "namespace", "name": "mcp__cua_repl", "tools": [{"type": "function", "name": "js", "description": "discovered"}]}
			]},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_2", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "tools": [{"type": "function", "name": "ping", "description": "again"}]}
			]}
		]
	}`
	declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))

	names := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		names = append(names, gjson.GetBytes(declaration, "name").String())
	}
	if len(names) != 3 || names[0] != "mcp__cua_repl__js" || names[1] != "tool_search" || names[2] != "fixture_tools__ping" {
		t.Fatalf("declarations = %v, want [mcp__cua_repl__js tool_search fixture_tools__ping]", names)
	}
	if got := gjson.GetBytes(declarations[0], "description").String(); got != "current" {
		t.Fatalf("top-level declaration must win over a discovered duplicate, got %q", got)
	}
	if got := forwardMap["fixture_tools__ping"]; got != "fixture_tools__ping" {
		t.Fatalf("forward map for discovered tool = %q", got)
	}
	identity := reverseMap["fixture_tools__ping"]
	if identity.Name != "ping" || identity.Namespace != "fixture_tools" {
		t.Fatalf("discovered identity = %+v, want ping in fixture_tools", identity)
	}
}
