package util

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func compareJSON(t *testing.T, expectedJSON, actualJSON string) {
	var expMap, actMap map[string]interface{}
	errExp := json.Unmarshal([]byte(expectedJSON), &expMap)
	errAct := json.Unmarshal([]byte(actualJSON), &actMap)

	if errExp != nil || errAct != nil {
		t.Fatalf("JSON Unmarshal error. Exp: %v, Act: %v", errExp, errAct)
	}

	if !reflect.DeepEqual(expMap, actMap) {
		expBytes, _ := json.MarshalIndent(expMap, "", "  ")
		actBytes, _ := json.MarshalIndent(actMap, "", "  ")
		t.Errorf("JSON mismatch:\nExpected:\n%s\n\nActual:\n%s", string(expBytes), string(actBytes))
	}
}

// ============================================================================
// Empty Schema Placeholder Tests
// ============================================================================

// ============================================================================
// Format field handling (ad-hoc patch removal)
// ============================================================================

func TestRemoveExtensionFields(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "removes x- fields at root",
			input: `{
				"type": "object",
				"x-custom-meta": "value",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "removes x- fields in nested properties",
			input: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string",
						"x-internal-id": 123
					}
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string"
					}
				}
			}`,
		},
		{
			name: "does NOT remove properties named x-",
			input: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number", "x-meta": "remove" }
				},
				"required": ["x-data"]
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number" }
				},
				"required": ["x-data"]
			}`,
		},
		{
			name: "does NOT remove $schema and other meta fields (as requested)",
			input: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles properties named $schema",
			input: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles escaping in paths",
			input: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string",
						"x-meta": "remove"
					}
				},
				"x-root.meta": "remove"
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string"
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := removeExtensionFields(tt.input)
			compareJSON(t, tt.expected, actual)
		})
	}
}

// TestIsPropertyDefinitionDistinguishesPropertyNamedProperties covers the classification that
// decides whether a key spelled like a schema keyword is a keyword or an author-chosen name.
// Matching a trailing ".properties" alone mistook the schema of a property named "properties" for
// a property map, which disabled cleaning inside it.
func TestIsPropertyDefinitionDistinguishesPropertyNamedProperties(t *testing.T) {
	for path, want := range map[string]bool{
		"":                                    false,
		"properties":                          true,
		"properties.properties":               false,
		"properties.properties.properties":    true,
		"properties.records.items.properties": true,
		"properties.records.items":            false,
		// Any prefix the caller nests the schema under must not change the answer.
		"schema.properties": true,
		"request.tools.0.functionDeclarations.0.parameters":                       false,
		"request.tools.0.functionDeclarations.0.parameters.properties":            true,
		"request.tools.0.functionDeclarations.0.parameters.properties.properties": false,
		// $defs and patternProperties are name maps for the same reason as properties.
		"$defs":                          true,
		"$defs.properties":               false,
		"properties.$defs":               false,
		"properties.a.patternProperties": true,
		"properties.patternProperties":   false,
	} {
		if got := isPropertyDefinition(path); got != want {
			t.Errorf("isPropertyDefinition(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSortByDepthUsesSegmentsAndIsStable(t *testing.T) {
	paths := []string{"root.verylong", "root.x.y", "first.same", "later.same"}
	sortByDepth(paths)
	want := []string{"root.x.y", "root.verylong", "first.same", "later.same"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("sortByDepth() = %v, want %v", paths, want)
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesAdditionalPropertiesAndPattern_Issue5959(t *testing.T) {
	input := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "SubmitTool",
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"recipient": {
				"type": "string",
				"pattern": "^(alice|bob)$",
				"minLength": 3,
				"maxLength": 10
			},
			"amount": {
				"type": "number",
				"minimum": 1
			},
			"nested": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"tag": {
						"type": "string",
						"pattern": "^[a-z]+$"
					}
				}
			},
			"items_list": {
				"type": "array",
				"items": {
					"type": "string",
					"pattern": "^[0-9]+$"
				}
			}
		},
		"required": ["recipient", "amount", "non_existent"]
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	if got := parsed.Get("additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.pattern"); !got.Exists() || got.String() != "^(alice|bob)$" {
		t.Fatalf("pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.minLength").Int(); got != 3 {
		t.Fatalf("minLength should be preserved as 3, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.maxLength").Int(); got != 10 {
		t.Fatalf("maxLength should be preserved as 10, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.amount.minimum").Int(); got != 1 {
		t.Fatalf("minimum should be preserved as 1, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("nested additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.pattern"); !got.Exists() || got.String() != "^[a-z]+$" {
		t.Fatalf("nested tag pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.items_list.items.pattern"); !got.Exists() || got.String() != "^[0-9]+$" {
		t.Fatalf("array items pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if parsed.Get("title").Exists() {
		t.Fatalf("title should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("$schema").Exists() {
		t.Fatalf("$schema should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("description").Exists() && parsed.Get("description").String() == "No extra properties allowed" {
		t.Fatalf("additionalProperties: false should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.recipient.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("nested pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.items_list.items.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("array items pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	// non_existent should be removed from required because it's not in properties
	required := parsed.Get("required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2. Cleaned: %s", len(required), cleaned)
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesSchemaValuedAdditionalProperties(t *testing.T) {
	input := `{
		"type": "object",
		"additionalProperties": {
			"type": "string",
			"pattern": "^[a-z]+$",
			"minLength": 2
		}
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	ap := parsed.Get("additionalProperties")
	if !ap.Exists() || !ap.IsObject() {
		t.Fatalf("additionalProperties object should be preserved, got: %s", cleaned)
	}
	if got := ap.Get("type").String(); got != "string" {
		t.Fatalf("additionalProperties.type = %q, want string", got)
	}
	if got := ap.Get("pattern").String(); got != "^[a-z]+$" {
		t.Fatalf("additionalProperties.pattern = %q, want ^[a-z]+$", got)
	}
	if got := ap.Get("minLength").Int(); got != 2 {
		t.Fatalf("additionalProperties.minLength = %d, want 2", got)
	}
	if ap.Get("description").Exists() && strings.Contains(ap.Get("description").String(), "pattern:") {
		t.Fatalf("additionalProperties pattern should not be converted to description hint: %s", cleaned)
	}
}

// TestIssue6732_GeminiCleanersInlineLocalDefs verifies that the Gemini cleaners expand local
// $defs references before the definition container is stripped, so nested fields survive.
func TestIssue6732_GeminiCleanersInlineLocalDefs(t *testing.T) {
	input := `{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"required":["person"],"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`

	for name, clean := range map[string]func(string) string{
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
			t.Errorf("%s: inlined properties.person.properties.name.type = %q, want %q: %s", name, gotType, "string", got)
		}
		if parsed.Get("$defs").Exists() {
			t.Errorf("%s: $defs should be removed after inlining: %s", name, got)
		}
		if strings.Contains(got, "See: Person") {
			t.Errorf("%s: resolvable local ref degraded to a hint: %s", name, got)
		}
	}
}

// TestIssue6732_GeminiCleanersInlinePropertyReuse verifies that a Zod-style #/properties/...
// reference is expanded by the Gemini cleaners instead of becoming a description hint.
func TestIssue6732_GeminiCleanersInlinePropertyReuse(t *testing.T) {
	input := `{"type":"object","properties":{"billing":{"type":"object","properties":{"zip":{"type":"string"}}},"shipping":{"$ref":"#/properties/billing"}}}`

	for name, clean := range map[string]func(string) string{
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.shipping.properties.zip.type").String(); gotType != "string" {
			t.Errorf("%s: properties.shipping.properties.zip.type = %q, want %q: %s", name, gotType, "string", got)
		}
	}
}

// TestIssue6732_GeminiCleanersReuseSameDefinitionTwice verifies that two properties referencing
// the same definition both keep the referenced fields; bounding expansion must not degrade
// ordinary reuse of a shared definition to a hint.
func TestIssue6732_GeminiCleanersReuseSameDefinitionTwice(t *testing.T) {
	input := `{"type":"object","properties":{"billing":{"$ref":"#/$defs/Address"},"shipping":{"$ref":"#/$defs/Address"}},"$defs":{"Address":{"type":"object","properties":{"zip":{"type":"string"}}}}}`

	for name, clean := range map[string]func(string) string{
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		for _, field := range []string{"billing", "shipping"} {
			if gotType := parsed.Get("properties." + field + ".properties.zip.type").String(); gotType != "string" {
				t.Errorf("%s: properties.%s.properties.zip.type = %q, want %q: %s", name, field, gotType, "string", got)
			}
		}
	}
}

// TestIssue6732_DefaultLiteralPreservedOnJSONSchemaRoute verifies that the parametersJsonSchema
// route forwards default values verbatim even when they contain schema-looking keys.
func TestIssue6732_DefaultLiteralPreservedOnJSONSchemaRoute(t *testing.T) {
	input := `{"type":"object","properties":{"report":{"type":"object","default":{"title":"Report","format":"pdf","id":7,"x-owner":"ops"}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	def := gjson.Parse(got).Get("properties.report.default")
	if !def.Exists() {
		t.Fatalf("default literal missing: %s", got)
	}
	if def.Get("title").String() != "Report" || def.Get("format").String() != "pdf" || def.Get("id").Int() != 7 || def.Get("x-owner").String() != "ops" {
		t.Errorf("default literal was rewritten, got %s: %s", def.Raw, got)
	}
}

// TestIssue6732_ExampleLiteralsPreserved verifies that example/examples values keep
// schema-looking keys on the parametersJsonSchema route.
func TestIssue6732_ExampleLiteralsPreserved(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"type":"string","example":{"title":"Hi","x-owner":"ops","id":3},"examples":[{"title":"Hi2","x-owner":"ops","id":4}]}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	example := parsed.Get("properties.a.example")
	if example.Get("title").String() != "Hi" || example.Get("x-owner").String() != "ops" || example.Get("id").Int() != 3 {
		t.Errorf("example literal was rewritten, got %s: %s", example.Raw, got)
	}
	exampleItem := parsed.Get("properties.a.examples.0")
	if exampleItem.Get("title").String() != "Hi2" || exampleItem.Get("x-owner").String() != "ops" || exampleItem.Get("id").Int() != 4 {
		t.Errorf("examples literal was rewritten, got %s: %s", exampleItem.Raw, got)
	}
}

// TestIssue6732_InlineLocalRefsLeavesLiteralRefs verifies that the inliner does not resolve
// {"$ref": ...} objects that appear as client data inside a default value.
func TestIssue6732_InlineLocalRefsLeavesLiteralRefs(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"type":"object","default":{"$ref":"#/$defs/Person"}}},"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if ref := parsed.Get("properties.a.default.$ref").String(); ref != "#/$defs/Person" {
		t.Errorf("literal $ref inside default = %q, want it preserved verbatim: %s", ref, got)
	}
	if parsed.Get("properties.a.default.properties").Exists() {
		t.Errorf("literal $ref inside default was expanded: %s", got)
	}
	if !parsed.Get("$defs.Person.properties.name").Exists() {
		t.Errorf("InlineLocalRefs must not strip definitions: %s", got)
	}
}

// TestIssue6732_PropertiesNamedDefaultAndTypeRemainSchemas verifies that properties literally
// named "default" and "type" keep their schema semantics: their own keywords are still cleaned
// and their own $ref is still inlined.
func TestIssue6732_PropertiesNamedDefaultAndTypeRemainSchemas(t *testing.T) {
	input := `{"type":"object","properties":{"default":{"$ref":"#/$defs/Person"},"type":{"type":"string","title":"Kind"}},"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.default.properties.name.type").String(); gotType != "string" {
		t.Errorf("property named \"default\" ref was not inlined, name type = %q: %s", gotType, got)
	}
	if parsed.Get("properties.type.title").Exists() {
		t.Errorf("property named \"type\" schema title should still be cleaned: %s", got)
	}
}

// TestIssue6732_ExpansionBudgetBoundsSharedDefs verifies that inlining a schema whose shared
// definitions fan out exponentially stays bounded instead of expanding without limit, while the
// affordable outer levels are still expanded.
func TestIssue6732_ExpansionBudgetBoundsSharedDefs(t *testing.T) {
	input := geminiSchemaDiamondInput()
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if len(got) >= 512*1024 {
		t.Errorf("diamond schema output = %d bytes, want < %d (input %d bytes)", len(got), 512*1024, len(input))
	}
	parsed := gjson.Parse(got)
	if !parsed.Get("properties.leaf").Exists() {
		t.Errorf("diamond schema lost the top-level property: %s", got)
	}
	if !parsed.Get("properties.leaf.properties.a.properties.a").Exists() {
		t.Errorf("diamond schema outer levels should still be expanded: %s", got)
	}
	if parsed.Get("$defs").Exists() {
		t.Errorf("diamond schema should not retain $defs: %s", got)
	}
}

// TestIssue6732_UnusedDefinitionsDoNotConsumeExpansionBudget verifies that definitions the
// cleaners are about to delete anyway neither consume the expansion budget nor make the kept
// fields depend on map iteration order.
func TestIssue6732_UnusedDefinitionsDoNotConsumeExpansionBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"required":["person"],"$defs":{"leaf0":{"type":"string","description":"leaf leaf leaf leaf leaf leaf leaf leaf leaf leaf"},"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	for i := range 13 {
		target := "leaf0"
		if i > 0 {
			target = "D" + strconv.Itoa(i-1)
		}
		sb.WriteString(`,"D` + strconv.Itoa(i) + `":{"type":"object","properties":{"a":{"$ref":"#/$defs/` + target + `"},"b":{"$ref":"#/$defs/` + target + `"}}}`)
	}
	sb.WriteString(`}}`)
	input := sb.String()

	first := CleanJSONSchemaForGeminiJSONSchema(input)
	for run := range 30 {
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		if got != first {
			t.Fatalf("run %d: output is not deterministic:\nfirst: %s\ngot:   %s", run, first, got)
		}
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
			t.Fatalf("run %d: used definition was not inlined, name type = %q: %s", run, gotType, got)
		}
		if parsed.Get("$defs").Exists() {
			t.Fatalf("run %d: $defs should be removed: %s", run, got)
		}
	}
}

// TestIssue6732_BudgetFallbackDoesNotCopyLargeDescriptions verifies that references degraded by
// the expansion budget do not each copy a large target description, which would defeat the bound.
func TestIssue6732_BudgetFallbackDoesNotCopyLargeDescriptions(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := range 200 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"p` + strconv.Itoa(i) + `":{"$ref":"#/$defs/Big"}`)
	}
	sb.WriteString(`},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("x", 32*1024) + `","properties":{"name":{"type":"string"}}}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	if len(got) >= 512*1024 {
		t.Errorf("shared large-description output = %d bytes, want < %d", len(got), 512*1024)
	}
	parsed := gjson.Parse(got)
	if !parsed.Get("properties.p0").Exists() || !parsed.Get("properties.p199").Exists() {
		t.Errorf("shared large-description schema lost properties: %s", got)
	}
	if gotType := parsed.Get("properties.p0.properties.name.type").String(); gotType != "string" {
		t.Errorf("first affordable expansion should keep its fields, name type = %q: %s", gotType, got)
	}
	if parsed.Get("$defs").Exists() {
		t.Errorf("$defs should be removed: %s", got)
	}
}

// TestIssue6732_NestedUnusedDefinitionsGraphDoesNotConsumeBudget verifies that a self-contained
// reference graph inside a nested definitions container (which cleaning deletes at every level)
// neither spends the expansion budget nor starves kept fields.
func TestIssue6732_NestedUnusedDefinitionsGraphDoesNotConsumeBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"},"a":{"type":"object","$defs":{"Leaf":{"type":"string","description":"` + strings.Repeat("x", 1024) + `"}`)
	for i := range 256 {
		sb.WriteString(`,"U` + strconv.Itoa(i) + `":{"$ref":"#/properties/a/$defs/Leaf"}`)
	}
	sb.WriteString(`}}},"required":["person"],"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
		t.Errorf("nested unused definitions starved the kept field, name type = %q: %s", gotType, got)
	}
	if parsed.Get("properties.a.$defs").Exists() || parsed.Get("$defs").Exists() {
		t.Errorf("nested and root $defs should be removed: %s", got)
	}
}

// TestIssue6732_RefSiblingNameMapReferencesStillExpand verifies that references inside a $ref
// node's own name-map siblings expand when the reference target itself expands.
func TestIssue6732_RefSiblingNameMapReferencesStillExpand(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/Base","properties":{"x":{"$ref":"#/$defs/S"}}}},"$defs":{"Base":{"type":"object"},"S":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.a.properties.x.type").String(); gotType != "string" {
		t.Errorf("ref sibling name-map reference lost its type, got %q: %s", gotType, got)
	}
}

// TestIssue6732_CleanInliningStripsDefinitionsFromIntermediate verifies that the cleaning
// inliner's intermediate output does not serialize definition containers (root or carried inside
// expanded targets), so repeated references to a target holding a huge unused definitions block
// cannot multiply that block in the intermediate JSON.
func TestIssue6732_CleanInliningStripsDefinitionsFromIntermediate(t *testing.T) {
	person := `{"type":"object","properties":{"name":{"type":"string"}},"$defs":{"Unused":{"type":"string","description":"` + strings.Repeat("x", 280*1024) + `"}}}`
	input := `{"type":"object","properties":{"p0":{"$ref":"#/$defs/Person"},"p1":{"$ref":"#/$defs/Person"}},"$defs":{"Person":` + person + `}}`

	got := inlineLocalRefsForClean(input)
	if len(got) >= 64*1024 {
		t.Errorf("clean inlining intermediate = %d bytes, want < %d", len(got), 64*1024)
	}
	parsed := gjson.Parse(got)
	if parsed.Get("$defs").Exists() {
		t.Errorf("intermediate should not carry root $defs: %s", got)
	}
	if parsed.Get("properties.p0.$defs").Exists() {
		t.Errorf("intermediate should not carry target-nested $defs: %s", got)
	}
	if gotType := parsed.Get("properties.p1.properties.name.type").String(); gotType != "string" {
		t.Errorf("second expansion lost its fields, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_ArrayRefWithExplicitTypeKeepsTargetItems verifies that the placeholder items
// Phase 0 adds to a typed but unresolved $ref node does not clobber the items of the referenced
// definition during inlining.
func TestIssue6732_ArrayRefWithExplicitTypeKeepsTargetItems(t *testing.T) {
	input := `{"type":"object","properties":{"rows":{"type":"array","$ref":"#/$defs/Rows"}},"$defs":{"Rows":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.rows.items.properties.name.type").String(); gotType != "string" {
		t.Errorf("array ref target items were clobbered, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_BudgetFallbackStillResolvesSiblings verifies that when a reference target is too
// large to expand, sibling subschemas on the same node are still resolved instead of being copied
// verbatim with their own references left dangling.
func TestIssue6732_BudgetFallbackStillResolvesSiblings(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/Big","properties":{"x":{"$ref":"#/$defs/S"}}}},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("x", 280*1024) + `"},"S":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.a.properties.x.type").String(); gotType != "string" {
		t.Errorf("sibling reference after budget fallback lost its type, got %q: %s", gotType, got)
	}
	if strings.Contains(got, `"$ref"`) {
		t.Errorf("cleaned schema left a dangling reference: %s", got)
	}
}

// TestIssue6732_InlineLocalRefsDefinitionNamesKeepSchemaSemantics verifies that definitions whose
// names look like schema keywords are treated as schemas by the public inliner: a definition named
// "properties" keeps its literal default intact, and a definition named "default" still expands.
func TestIssue6732_InlineLocalRefsDefinitionNamesKeepSchemaSemantics(t *testing.T) {
	input := `{"$defs":{"S":{"type":"string"},"properties":{"type":"object","default":{"$ref":"#/$defs/S"}},"default":{"type":"object","properties":{"x":{"$ref":"#/$defs/S"}}}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if ref := parsed.Get("$defs.properties.default.$ref").String(); ref != "#/$defs/S" {
		t.Errorf("literal default inside definition named \"properties\" was expanded, $ref = %q: %s", ref, got)
	}
	if gotType := parsed.Get("$defs.default.properties.x.type").String(); gotType != "string" {
		t.Errorf("definition named \"default\" was not resolved as a schema, x type = %q: %s", gotType, got)
	}
}

// TestIssue6732_TargetNestedDefinitionsExemptFromBudget verifies that unused definitions nested
// inside a referenced target (which cleaning deletes) do not inflate the target's budget charge.
func TestIssue6732_TargetNestedDefinitionsExemptFromBudget(t *testing.T) {
	person := `{"type":"object","properties":{"name":{"type":"string"}},"$defs":{"Unused":{"type":"string","description":"` + strings.Repeat("x", 280*1024) + `"}}}`
	cases := map[string]string{
		"propertyRef": `{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"$defs":{"Person":` + person + `}}`,
		"rootRef":     `{"$ref":"#/$defs/Person","$defs":{"Person":` + person + `}}`,
	}
	for caseName, input := range cases {
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		parsed := gjson.Parse(got)
		namePath := "properties.person.properties.name.type"
		if caseName == "rootRef" {
			namePath = "properties.name.type"
		}
		if gotType := parsed.Get(namePath).String(); gotType != "string" {
			t.Errorf("%s: %s = %q, want %q: %s", caseName, namePath, gotType, "string", got)
		}
		if parsed.Get("$defs").Exists() {
			t.Errorf("%s: $defs should be removed: %s", caseName, got)
		}
	}
}

// TestIssue6732_SmallTargetFanOutStaysBounded verifies that thousands of references to a small
// target with a near-limit description cannot multiply the description without bound through a
// mix of expansions and fallback copies.
func TestIssue6732_SmallTargetFanOutStaysBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := range 2000 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"p` + strconv.Itoa(i) + `":{"$ref":"#/$defs/S"}`)
	}
	sb.WriteString(`},"$defs":{"S":{"type":"string","description":"` + strings.Repeat("x", 512) + `"}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	if len(got) >= 512*1024 {
		t.Errorf("small-target fan-out output = %d bytes, want < %d", len(got), 512*1024)
	}
}

// TestIssue6732_EscapedDescriptionFanOutStaysBounded verifies that budget accounting covers
// HTML-escaped string growth during JSON encoding.
func TestIssue6732_EscapedDescriptionFanOutStaysBounded(t *testing.T) {
	input := `{"type":"object","properties":{"p0":{"$ref":"#/$defs/Big"},"p1":{"$ref":"#/$defs/Big"},"p2":{"$ref":"#/$defs/Big"},"p3":{"$ref":"#/$defs/Big"},"p4":{"$ref":"#/$defs/Big"},"p5":{"$ref":"#/$defs/Big"},"p6":{"$ref":"#/$defs/Big"},"p7":{"$ref":"#/$defs/Big"}},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("<", 32*1024) + `","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if len(got) >= 512*1024 {
		t.Errorf("escaped-description fan-out output = %d bytes, want < %d", len(got), 512*1024)
	}
}

// TestIssue6732_DependenciesNameMapKeepsSchemaSemantics verifies that the Draft-07 dependencies
// container behaves as a name map: a dependency keyed "default" holds a schema whose references
// still expand in the public inliner.
func TestIssue6732_DependenciesNameMapKeepsSchemaSemantics(t *testing.T) {
	input := `{"type":"object","dependencies":{"default":{"properties":{"x":{"$ref":"#/definitions/S"}}}},"definitions":{"S":{"type":"string"}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("dependencies.default.properties.x.type").String(); gotType != "string" {
		t.Errorf("dependency schema named \"default\" was not resolved, x type = %q: %s", gotType, got)
	}
	if parsed.Get("dependencies.default.properties.x.$ref").Exists() {
		t.Errorf("dependency schema reference was left dangling: %s", got)
	}
}

// TestIssue6732_OmittedFallbackDescriptionNotCharged verifies that a target description the
// fallback omits (because it exceeds the copy limit) is not charged to the expansion budget, so
// a following small reference still expands.
func TestIssue6732_OmittedFallbackDescriptionNotCharged(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"},"b":{"$ref":"#/$defs/B"}},"$defs":{"A":{"type":"string","description":"` + strings.Repeat("x", 262142) + `"},"B":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.b.properties.name.type").String(); gotType != "string" {
		t.Errorf("small reference after omitted fallback description lost its fields, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_SynthesizedTypeDoesNotOverrideRefTarget verifies that the type Phase 0 would
// infer from a ref node's sibling items does not clobber the referenced definition's object
// type, and that cleaning the result again is stable.
func TestIssue6732_SynthesizedTypeDoesNotOverrideRefTarget(t *testing.T) {
	input := `{"$ref":"#/$defs/O","items":{"type":"integer"},"$defs":{"O":{"type":"object","properties":{"x":{"type":"string"}}}}}`

	first := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(first)
	if gotType := parsed.Get("type").String(); gotType != "object" {
		t.Errorf("root ref target type = %q, want %q: %s", gotType, "object", first)
	}
	if parsed.Get("items").Exists() {
		t.Errorf("object schema should not carry items: %s", first)
	}
	if gotType := parsed.Get("properties.x.type").String(); gotType != "string" {
		t.Errorf("target property lost its type, got %q: %s", gotType, first)
	}
	if second := CleanJSONSchemaForGeminiJSONSchema(first); second != first {
		t.Errorf("cleaning is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestIssue6732_PropertyNamedRefRemainsProperty verifies that a property literally named "$ref"
// is treated as a property, not as a reference keyword, by the cleaner.
func TestIssue6732_PropertyNamedRefRemainsProperty(t *testing.T) {
	input := `{"type":"object","properties":{"$ref":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get(`properties.\$ref.type`).String(); gotType != "string" {
		t.Errorf("property named $ref lost its schema, type = %q: %s", gotType, got)
	}
	if strings.Contains(got, "See:") {
		t.Errorf("property named $ref was converted into a hint: %s", got)
	}
}

// TestIssue6732_PropertyNamedConstRemainsProperty verifies that a property literally named
// "const" holding a schema object is not converted into an enum constraint on its parent.
func TestIssue6732_PropertyNamedConstRemainsProperty(t *testing.T) {
	input := `{"type":"object","properties":{"const":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get(`properties.const.type`).String(); gotType != "string" {
		t.Errorf("property named const lost its schema, type = %q: %s", gotType, got)
	}
	if parsed.Get("enum").Exists() {
		t.Errorf("property named const fabricates a root enum: %s", got)
	}
}

// geminiSchemaDiamondInput builds a schema referencing D12, where each Di is an object with two
// properties referencing Di-1 and D0 references a leaf definition twice. Fully expanding it
// produces 2^13 leaf copies.
func geminiSchemaDiamondInput() string {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"leaf":{"$ref":"#/$defs/D12"}},"$defs":{"leaf0":{"type":"string","description":"leaf leaf leaf leaf leaf leaf leaf leaf leaf leaf"}`)
	for i := range 13 {
		target := "leaf0"
		if i > 0 {
			target = "D" + strconv.Itoa(i-1)
		}
		sb.WriteString(`,"D` + strconv.Itoa(i) + `":{"type":"object","properties":{"a":{"$ref":"#/$defs/` + target + `"},"b":{"$ref":"#/$defs/` + target + `"}}}`)
	}
	sb.WriteString(`}}`)
	return sb.String()
}

// TestCleanJSONSchemaStripsPropertyNamesUnderPropertyNamedProperties covers the reported failure:
// the private Gemini backend rejects "propertyNames" with an unknown-field 400, and MCP tool
// schemas place it inside a property that is itself named "properties".
func TestCleanJSONSchemaStripsPropertyNamesUnderPropertyNamedProperties(t *testing.T) {
	shapes := map[string]string{
		// Nested in an array item, alongside the item's own properties map.
		"arrayItem": `{"type":"object","properties":{"records":{"type":"array","items":{"type":"object",` +
			`"properties":{"name":{"type":"string"}},"propertyNames":{"type":"string"}}}}}`,
		// A dynamic map declared by a property named "properties".
		"propertyNamedProperties": `{"type":"object","properties":{"properties":{"type":"object",` +
			`"propertyNames":{"type":"string"}}}}`,
		// Both shapes combined, as the reported tool schemas did.
		"combined": `{"type":"object","properties":{"pages":{"type":"array","items":{"type":"object",` +
			`"properties":{"properties":{"type":"object","propertyNames":{"type":"string"},` +
			`"additionalProperties":true}},"propertyNames":{"type":"string"}}}}}`,
	}

	for name, schema := range shapes {
		for cleaner, clean := range map[string]func(string) string{} {
			got := clean(schema)
			if strings.Contains(got, `"propertyNames"`) {
				t.Errorf("%s/%s: propertyNames survived cleaning: %s", name, cleaner, got)
			}
			if strings.Contains(got, `"additionalProperties"`) {
				t.Errorf("%s/%s: additionalProperties survived cleaning: %s", name, cleaner, got)
			}
		}
	}
}

// TestCleanJSONSchemaKeepsPropertiesNamedLikeKeywords guards the other half of the rule: a schema
// may legitimately declare properties named after schema keywords, and those must survive.
func TestCleanJSONSchemaKeepsPropertiesNamedLikeKeywords(t *testing.T) {
	input := `{"type":"object","properties":{
		"propertyNames":{"type":"string"},
		"patternProperties":{"type":"string"},
		"properties":{"type":"object","properties":{"propertyNames":{"type":"string"}}}
	}}`

	for cleaner, clean := range map[string]func(string) string{} {
		got := gjson.Parse(clean(input))
		for _, path := range []string{
			"properties.propertyNames",
			"properties.patternProperties",
			"properties.properties.properties.propertyNames",
		} {
			if !got.Get(path).Exists() {
				t.Errorf("%s: property %s was removed: %s", cleaner, path, got.Raw)
			}
		}
	}
}

func TestCleanJSONSchema_ConditionalKeywords(t *testing.T) {
	// 1. Root-level if/then/else
	rootInput := `{
		"type": "object",
		"properties": { "kind": { "type": "string", "enum": ["buy", "sell"] } },
		"required": ["kind"],
		"if":   { "properties": { "kind": { "const": "sell" } } },
		"then": { "properties": { "sell_reason": { "type": "string", "description": "why the position is being sold" } }, "required": ["sell_reason"] },
		"else": { "properties": { "buy_reason": { "type": "string" } } }
	}`

	for name, clean := range map[string]func(string) string{} {
		res := gjson.Parse(clean(rootInput))
		if res.Get("if").Exists() {
			t.Errorf("[%s] root 'if' was not removed: %s", name, res.Raw)
		}
		if res.Get("then").Exists() {
			t.Errorf("[%s] root 'then' was not removed: %s", name, res.Raw)
		}
		if res.Get("else").Exists() {
			t.Errorf("[%s] root 'else' was not removed: %s", name, res.Raw)
		}
		if !res.Get("properties.sell_reason").Exists() {
			t.Errorf("[%s] then.properties.sell_reason was lost: %s", name, res.Raw)
		}
		if !res.Get("properties.buy_reason").Exists() {
			t.Errorf("[%s] else.properties.buy_reason was lost: %s", name, res.Raw)
		}
		if res.Get("properties.sell_reason.description").String() != "why the position is being sold" {
			t.Errorf("[%s] sell_reason description mismatch: %s", name, res.Raw)
		}
	}

	// 2. allOf with if/then
	allOfInput := `{
		"type": "object",
		"properties": { "kind": { "type": "string", "enum": ["buy", "sell"] } },
		"required": ["kind"],
		"allOf": [
			{
				"if":   { "properties": { "kind": { "const": "sell" } } },
				"then": {
					"properties": { "sell_reason": { "type": "string", "description": "why the position is being sold" } },
					"required": ["sell_reason"]
				}
			}
		]
	}`

	for name, clean := range map[string]func(string) string{} {
		res := gjson.Parse(clean(allOfInput))
		if res.Get("allOf").Exists() {
			t.Errorf("[%s] 'allOf' was not removed: %s", name, res.Raw)
		}
		if res.Get("if").Exists() || strings.Contains(res.Raw, `"if":`) {
			t.Errorf("[%s] 'if' keyword present: %s", name, res.Raw)
		}
		if !res.Get("properties.sell_reason").Exists() {
			t.Errorf("[%s] allOf.then.properties.sell_reason was lost: %s", name, res.Raw)
		}
		if res.Get("properties.sell_reason.description").String() != "why the position is being sold" {
			t.Errorf("[%s] sell_reason description mismatch: %s", name, res.Raw)
		}
	}

	// 3. Nested property with if/then
	nestedInput := `{
		"type": "object",
		"properties": {
			"trade": {
				"type": "object",
				"properties": { "kind": { "type": "string" } },
				"if":   { "properties": { "kind": { "const": "sell" } } },
				"then": { "properties": { "sell_reason": { "type": "string" } } }
			}
		}
	}`

	for name, clean := range map[string]func(string) string{} {
		res := gjson.Parse(clean(nestedInput))
		if res.Get("properties.trade.if").Exists() {
			t.Errorf("[%s] nested 'if' was not removed: %s", name, res.Raw)
		}
		if res.Get("properties.trade.then").Exists() {
			t.Errorf("[%s] nested 'then' was not removed: %s", name, res.Raw)
		}
		if !res.Get("properties.trade.properties.sell_reason").Exists() {
			t.Errorf("[%s] nested then.properties.sell_reason was lost: %s", name, res.Raw)
		}
	}
}
