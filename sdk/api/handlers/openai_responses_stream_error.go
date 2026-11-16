package handlers

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string         `json:"type"`
	Error          map[string]any `json:"error"`
	SequenceNumber int            `json:"sequence_number"`
}

// openAIResponsesStreamErrorClass pairs the OpenAI error code and the OpenAI
// error type that a given HTTP status is reported as.
//
// The two are kept together deliberately. They are halves of one classification
// and a client reads both: the code names the condition, the type says whose
// fault it was and therefore whether a retry is allowed. Deriving them in two
// places let them contradict each other, which is how a Codex stream that was
// cut in transit came to be reported with the retryable code "request_timeout"
// alongside the type "invalid_request_error" — a type that tells every
// downstream consumer the request was malformed and must not be replayed.
type openAIResponsesStreamErrorClass struct {
	code      string
	errorType string
}

const (
	openAIResponsesClientErrorType = "invalid_request_error"
	openAIResponsesServerErrorType = "server_error"
)

// openAIResponsesStreamErrorClasses holds the statuses that do not follow the
// plain "4xx is the caller's fault, 5xx is ours" split.
//
// StatusRequestTimeout is the reason this table exists. A Codex stream that ends
// before response.completed is surfaced with that status, and the request itself
// was well formed: the connection failed, so the caller may replay it.
var openAIResponsesStreamErrorClasses = map[int]openAIResponsesStreamErrorClass{
	http.StatusUnauthorized:    {code: "invalid_api_key", errorType: openAIResponsesClientErrorType},
	http.StatusForbidden:       {code: "insufficient_quota", errorType: openAIResponsesClientErrorType},
	http.StatusTooManyRequests: {code: "rate_limit_exceeded", errorType: openAIResponsesClientErrorType},
	http.StatusNotFound:        {code: "model_not_found", errorType: openAIResponsesClientErrorType},
	http.StatusRequestTimeout:  {code: "request_timeout", errorType: openAIResponsesServerErrorType},
}

func openAIResponsesStreamErrorClassFor(status int) openAIResponsesStreamErrorClass {
	if class, ok := openAIResponsesStreamErrorClasses[status]; ok {
		return class
	}
	if status >= http.StatusInternalServerError {
		return openAIResponsesStreamErrorClass{code: "internal_server_error", errorType: openAIResponsesServerErrorType}
	}
	if status >= http.StatusBadRequest {
		return openAIResponsesStreamErrorClass{code: "invalid_request_error", errorType: openAIResponsesClientErrorType}
	}
	return openAIResponsesStreamErrorClass{code: "unknown_error", errorType: openAIResponsesClientErrorType}
}

func openAIResponsesStreamErrorCode(status int) string {
	return openAIResponsesStreamErrorClassFor(status).code
}

func openAIResponsesStreamErrorType(status int) string {
	return openAIResponsesStreamErrorClassFor(status).errorType
}

// BuildOpenAIResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
// It matches the official responses streaming event shape where the error details are nested.
func BuildOpenAIResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	doc := []byte(`{"type":"error","error":{},"sequence_number":0}`)
	doc = populateOpenAIResponsesStreamErrorChunk(doc, "error", status, errText, sequenceNumber)
	if data := doc; len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","error":{"type":"server_error","code":"internal_server_error","message":"internal error","param":null},"sequence_number":0}`)
}

// BuildOpenAIResponsesStreamFailedChunk builds the terminal Responses event used by official Codex clients.
// It is intentionally separate from BuildOpenAIResponsesStreamErrorChunk so existing clients keep the legacy shape.
func BuildOpenAIResponsesStreamFailedChunk(status int, errText string, sequenceNumber int) []byte {
	doc := []byte(`{"type":"response.failed","sequence_number":0,"response":{"status":"failed","error":{}}}`)
	doc = populateOpenAIResponsesStreamErrorChunk(doc, "response.error", status, errText, sequenceNumber)
	if data := doc; len(data) > 0 {
		return data
	}
	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"status":"failed","error":{"type":"server_error","code":"internal_server_error","message":"internal error"}}}`)
}

func populateOpenAIResponsesStreamErrorChunk(doc []byte, errorPath string, status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}

	code := openAIResponsesStreamErrorCode(status)
	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	errorType := openAIResponsesStreamErrorType(status)
	var paramRaw string
	hasParam := false

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && gjson.Valid(trimmed) {
		root := gjson.Parse(trimmed)
		if root.IsObject() {
			if seqNode := root.Get("sequence_number"); seqNode.Type == gjson.Number {
				sequenceNumber = int(seqNode.Int())
			}
			doc, _ = sjson.SetBytes(doc, "sequence_number", sequenceNumber)

			hasScalarMessage := false
			for _, path := range []string{"error", "response.error", "body.error"} {
				errNode := root.Get(path)
				if !errNode.Exists() || errNode.Type == gjson.Null {
					continue
				}
				if errNode.IsObject() {
					doc, _ = sjson.SetRawBytes(doc, errorPath, []byte(errNode.Raw))
					return applyOpenAIResponsesNestedErrorCode(doc, errorPath)
				}
				if errNode.Type == gjson.String && !hasScalarMessage {
					if scalar := strings.TrimSpace(errNode.String()); scalar != "" {
						message = scalar
					} else {
						message = http.StatusText(status)
					}
					hasScalarMessage = true
				}
			}

			if msgNode := root.Get("message"); msgNode.Type == gjson.String && strings.TrimSpace(msgNode.String()) != "" {
				message = strings.TrimSpace(msgNode.String())
			}
			hasTopLevelCode := false
			if codeNode := root.Get("code"); codeNode.Exists() && codeNode.Type != gjson.Null {
				if c := strings.TrimSpace(codeNode.String()); c != "" {
					code = c
					hasTopLevelCode = true
				}
			}
			if hasTopLevelCode && !hasScalarMessage && strings.TrimSpace(root.Get("message").String()) == "" {
				fallback := openAIResponsesPolicyFallbackMessage(code)
				if fallback == "" {
					fallback = code
				}
				message = fallback
			}
			if typeNode := root.Get("type"); typeNode.Type == gjson.String {
				if t := strings.TrimSpace(typeNode.String()); t != "" && t != "error" && t != "response.failed" && t != "response.error" {
					errorType = t
				}
			}
			if paramNode := root.Get("param"); paramNode.Exists() {
				paramRaw = paramNode.Raw
				hasParam = true
			}
		}
	}

	doc, _ = sjson.SetBytes(doc, "sequence_number", sequenceNumber)
	doc, _ = sjson.SetRawBytes(doc, errorPath, []byte(`{"type":"","code":"","message":"","param":null}`))
	doc, _ = sjson.SetBytes(doc, errorPath+".type", errorType)
	doc, _ = sjson.SetBytes(doc, errorPath+".code", code)
	doc, _ = sjson.SetBytes(doc, errorPath+".message", message)
	if hasParam {
		doc, _ = sjson.SetRawBytes(doc, errorPath+".param", []byte(paramRaw))
	}
	return doc
}

func openAIResponsesPolicyFallbackMessage(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "bio_policy":
		return "This content was flagged for possible biological risk."
	case "cyber_policy", "cyber_policy_violation":
		return "This request has been flagged for possible cybersecurity risk."
	case "misalignment_policy_violation":
		return "This request was blocked due to a misalignment policy violation."
	case "invalid_prompt":
		return "Invalid request."
	default:
		return ""
	}
}

func applyOpenAIResponsesNestedErrorCode(doc []byte, errorPath string) []byte {
	if codeNode := gjson.GetBytes(doc, errorPath+".code"); codeNode.Exists() && codeNode.Type != gjson.Null {
		if code := strings.TrimSpace(codeNode.String()); code != "" {
			if strings.TrimSpace(gjson.GetBytes(doc, errorPath+".message").String()) == "" {
				fallback := openAIResponsesPolicyFallbackMessage(code)
				if fallback == "" {
					fallback = code
				}
				doc, _ = sjson.SetBytes(doc, errorPath+".message", fallback)
			}
			return doc
		}
	}
	// Anthropic reports input overflow by message only; Responses clients key on this code.
	if gjson.GetBytes(doc, errorPath+".type").String() == "invalid_request_error" &&
		strings.HasPrefix(gjson.GetBytes(doc, errorPath+".message").String(), "prompt is too long") {
		doc, _ = sjson.SetBytes(doc, errorPath+".code", "context_length_exceeded")
	}
	return doc
}
