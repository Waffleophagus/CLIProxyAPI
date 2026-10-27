package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func normalizedTerminationStatus(statusCode int) int {
	if statusCode < http.StatusOK || statusCode > 599 {
		return http.StatusForbidden
	}
	return statusCode
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func nextStreamChunk(ctx context.Context, closed *bool, chunks <-chan coreexecutor.StreamChunk) (coreexecutor.StreamChunk, bool, bool) {
	if closed != nil && *closed {
		return coreexecutor.StreamChunk{}, false, false
	}
	var chunk coreexecutor.StreamChunk
	var ok bool
	if ctx != nil {
		select {
		case <-ctx.Done():
			return coreexecutor.StreamChunk{}, false, true
		case chunk, ok = <-chunks:
			// A channel closed together with the cancellation was cut by it: the select above picks at random when both are
			// ready, and the plain close would be reported as a success.
			if !ok && ctx.Err() != nil {
				return coreexecutor.StreamChunk{}, false, true
			}
		}
	} else {
		chunk, ok = <-chunks
	}
	if !ok && closed != nil {
		*closed = true
	}
	return chunk, ok, false
}

func downstreamHeadersFromExecutor(headers http.Header, passthrough bool) http.Header {
	if !passthrough {
		return nil
	}
	return FilterUpstreamHeaders(headers)
}

// ModelDetailIDContextKey requests selection of a single model from the final catalog.
const ModelDetailIDContextKey = "cliproxy.model_detail_id"

// WriteModelListResponse serializes the model-list payload and writes the
// resulting headers and body to the Gin context.
func (h *BaseAPIHandler) WriteModelListResponse(c *gin.Context, sourceFormat string, payload any) {
	if c == nil {
		return
	}
	var body []byte
	switch v := payload.(type) {
	case []byte:
		body = cloneBytes(v)
	default:
		var errMarshal error
		body, errMarshal = json.Marshal(payload)
		if errMarshal != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errMarshal.Error()})
			return
		}
	}

	if modelID, detail := c.Get(ModelDetailIDContextKey); detail {
		c.Writer.Header().Del("Content-Length")
		var catalog struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if errUnmarshal := json.Unmarshal(body, &catalog); errUnmarshal != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "Invalid model catalog", "type": "api_error"}})
			return
		}
		var selected json.RawMessage
		for _, model := range append(catalog.Data, catalog.Models...) {
			var entry struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			}
			if errUnmarshal := json.Unmarshal(model, &entry); errUnmarshal != nil {
				continue
			}
			if entry.ID == "" {
				entry.ID = entry.Slug
			}
			if entry.ID != "" && entry.ID == modelID {
				selected = model
				break
			}
		}
		if selected == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "Model not found", "type": "invalid_request_error", "code": "model_not_found"}})
			return
		}
		body = selected
	}
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Header("Content-Type", "application/json; charset=utf-8")
	}
	c.Set("API_RESPONSE", cloneBytes(body))
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(body)
}
