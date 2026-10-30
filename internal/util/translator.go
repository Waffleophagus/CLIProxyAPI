// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for JSON manipulation, proxy configuration,
// and other common operations used across the application.
package util

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// WalkSchema is Walk for JSON Schema documents: it records the field wherever a schema keyword
// may appear, but does not descend into literal values (default, example, examples, const, enum),
// whose contents are client data rather than subschemas. The literal keyword itself is still
// recorded when it is the searched field, and under a property-name map a key spelled like one is
// a property name whose subschema is still walked.
func WalkSchema(value gjson.Result, path, field string, paths *[]string) {
	walk(value, path, field, paths, true)
}

func walk(value gjson.Result, path, field string, paths *[]string, schemaAware bool) {
	switch value.Type {
	case gjson.JSON:
		// For JSON objects and arrays, iterate through each child
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			// Escape special characters for gjson/sjson path syntax
			// . -> \.
			// * -> \*
			// ? -> \?
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)

			if path == "" {
				childPath = safeKey
			} else {
				childPath = path + "." + safeKey
			}
			if keyStr == field {
				*paths = append(*paths, childPath)
			}
			if schemaAware && isLiteralValueKeyword(keyStr) && !isPropertyDefinition(path) {
				return true
			}
			walk(val, childPath, field, paths, schemaAware)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// Terminal types - no further traversal needed
	}
}

// FixJSON converts non-standard JSON that uses single quotes for strings into
// RFC 8259-compliant JSON by converting those single-quoted strings to
// double-quoted strings with proper escaping.
//
// Examples:
//
//	{'a': 1, 'b': '2'}      => {"a": 1, "b": "2"}
//	{"t": 'He said "hi"'} => {"t": "He said \"hi\""}
//
// Rules:
//   - Existing double-quoted JSON strings are preserved as-is.
//   - Single-quoted strings are converted to double-quoted strings.
//   - Inside converted strings, any double quote is escaped (\").
//   - Common backslash escapes (\n, \r, \t, \b, \f, \\) are preserved.
//   - \' inside single-quoted strings becomes a literal ' in the output (no
//     escaping needed inside double quotes).
//   - Unicode escapes (\uXXXX) inside single-quoted strings are forwarded.
//   - The function does not attempt to fix other non-JSON features beyond quotes.
func FixJSON(input string) string {
	var out bytes.Buffer

	inDouble := false
	inSingle := false
	escaped := false // applies within the current string state

	// Helper to write a rune, escaping double quotes when inside a converted
	// single-quoted string (which becomes a double-quoted string in output).
	writeConverted := func(r rune) {
		if r == '"' {
			out.WriteByte('\\')
			out.WriteByte('"')
			return
		}
		out.WriteRune(r)
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if inDouble {
			out.WriteRune(r)
			if escaped {
				// end of escape sequence in a standard JSON string
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDouble = false
			}
			continue
		}

		if inSingle {
			if escaped {
				// Handle common escape sequences after a backslash within a
				// single-quoted string
				escaped = false
				switch r {
				case 'n', 'r', 't', 'b', 'f', '/', '"':
					// Keep the backslash and the character (except for '"' which
					// rarely appears, but if it does, keep as \" to remain valid)
					out.WriteByte('\\')
					out.WriteRune(r)
				case '\\':
					out.WriteByte('\\')
					out.WriteByte('\\')
				case '\'':
					// \' inside single-quoted becomes a literal '
					out.WriteRune('\'')
				case 'u':
					// Forward \uXXXX if possible
					out.WriteByte('\\')
					out.WriteByte('u')
					// Copy up to next 4 hex digits if present
					for k := 0; k < 4 && i+1 < len(runes); k++ {
						peek := runes[i+1]
						// simple hex check
						if (peek >= '0' && peek <= '9') || (peek >= 'a' && peek <= 'f') || (peek >= 'A' && peek <= 'F') {
							out.WriteRune(peek)
							i++
						} else {
							break
						}
					}
				default:
					// Unknown escape: preserve the backslash and the char
					out.WriteByte('\\')
					out.WriteRune(r)
				}
				continue
			}

			if r == '\\' { // start escape sequence
				escaped = true
				continue
			}
			if r == '\'' { // end of single-quoted string
				out.WriteByte('"')
				inSingle = false
				continue
			}
			// regular char inside converted string; escape double quotes
			writeConverted(r)
			continue
		}

		// Outside any string
		if r == '"' {
			inDouble = true
			out.WriteRune(r)
			continue
		}
		if r == '\'' { // start of non-standard single-quoted string
			inSingle = true
			out.WriteByte('"')
			continue
		}
		out.WriteRune(r)
	}

	// If input ended while still inside a single-quoted string, close it to
	// produce the best-effort valid JSON.
	if inSingle {
		out.WriteByte('"')
	}

	return out.String()
}

func CanonicalToolName(name string) string {
	canonical := strings.TrimSpace(name)
	canonical = strings.TrimLeft(canonical, "_")
	return strings.ToLower(canonical)
}

// ToolNameMapFromClaudeRequest returns a canonical-name -> original-name map extracted from a Claude request.
// It is used to restore exact tool name casing for clients that require strict tool name matching (e.g. Claude Code).
func ToolNameMapFromClaudeRequest(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	hasTools := tools.Exists() && tools.IsArray()
	hasToolAddition := claudePayloadMayContainToolAddition(rawJSON)
	if !hasTools && !hasToolAddition {
		return nil
	}

	var out map[string]string
	recordTool := func(tool gjson.Result) {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			return
		}
		key := CanonicalToolName(name)
		if key == "" {
			return
		}
		if out == nil {
			out = make(map[string]string, 4)
		}
		if _, exists := out[key]; !exists {
			out[key] = name
		}
	}

	if hasTools {
		tools.ForEach(func(_, tool gjson.Result) bool {
			recordTool(tool)
			return true
		})
	}
	if hasToolAddition {
		forEachClaudeInlineToolAdditionDefinition(rawJSON, recordTool)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

func claudePayloadMayContainToolAddition(rawJSON []byte) bool {
	return bytes.Contains(rawJSON, []byte("tool_addition")) ||
		bytes.Contains(rawJSON, []byte(`\u`)) ||
		bytes.Contains(rawJSON, []byte(`\U`))
}

// forEachClaudeInlineToolAdditionDefinition visits the tool definitions carried by
// mid-conversation tool_addition blocks in role=system messages.
func forEachClaudeInlineToolAdditionDefinition(rawJSON []byte, fn func(def gjson.Result)) {
	if !claudePayloadMayContainToolAddition(rawJSON) {
		return
	}
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "system" {
			return true
		}
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "tool_addition" {
				return true
			}
			toolSpec := part.Get("tool")
			if toolSpec.Get("type").String() == "tool_definition" || toolSpec.Get("definition").IsObject() {
				if def := toolSpec.Get("definition"); def.IsObject() {
					fn(def)
				}
			}
			return true
		})
		return true
	})
}

func MapToolName(toolNameMap map[string]string, name string) string {
	if name == "" || toolNameMap == nil {
		return name
	}
	if mapped, ok := toolNameMap[CanonicalToolName(name)]; ok && mapped != "" {
		return mapped
	}
	return name
}
