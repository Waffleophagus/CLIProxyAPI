package helps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	claudeWebSearchToolResultMarker = []byte(`"web_search_tool_result"`)
	claudeWebSearchCitationMarker   = []byte(`"web_search_result_location"`)
)

// WithClaudeWebSearchResultRestore attaches a request-scoped cache lookup so
// Responses -> Claude request translation can restore web_search_tool_result
// blocks before deciding whether assistant citations remain backed by results.
func WithClaudeWebSearchResultRestore(ctx context.Context, sessionKey string, ttl ...time.Duration) context.Context {
	if strings.TrimSpace(sessionKey) == "" {
		return ctx
	}
	return responsesclaude.WithWebSearchResultLookup(ctx, func(toolUseID string) ([]byte, bool) {
		cached, ok, errGet := cache.GetClaudeWebSearchResultRequired(ctx, sessionKey, toolUseID, ttl...)
		if errGet != nil || !ok || len(cached) == 0 {
			return nil, false
		}
		return cached, true
	})
}

// RecordClaudeWebSearchResults stores the web_search_tool_result blocks of a
// Claude response for sessionKey. data is either a complete Messages response
// or SSE text (one or more lines); the result block arrives whole in its
// content_block_start event.
func RecordClaudeWebSearchResults(sessionKey string, data []byte, ttl time.Duration, limit int) {
	RecordClaudeWebSearchResultsContext(context.Background(), sessionKey, data, ttl, limit)
}

// RecordClaudeWebSearchResultsContext stores the web_search_tool_result blocks
// of a Claude response for sessionKey.
func RecordClaudeWebSearchResultsContext(ctx context.Context, sessionKey string, data []byte, ttl time.Duration, limit int) {
	if strings.TrimSpace(sessionKey) == "" || !bytes.Contains(data, claudeWebSearchToolResultMarker) {
		return
	}
	if content := gjson.GetBytes(data, "content"); content.IsArray() {
		content.ForEach(func(_, block gjson.Result) bool {
			recordClaudeWebSearchResultBlock(ctx, sessionKey, block, ttl, limit)
			return true
		})
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := gjson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
		if event.Get("type").String() == "content_block_start" {
			recordClaudeWebSearchResultBlock(ctx, sessionKey, event.Get("content_block"), ttl, limit)
		}
	}
}

func recordClaudeWebSearchResultBlock(ctx context.Context, sessionKey string, block gjson.Result, ttl time.Duration, limit int) {
	if block.Get("type").String() != "web_search_tool_result" {
		return
	}
	content := block.Get("content")
	if !content.Exists() || claudeWebSearchResultContentEmpty(content) {
		return
	}
	cache.CacheClaudeWebSearchResultBestEffort(ctx, sessionKey, block.Get("tool_use_id").String(), []byte(content.Raw), ttl, limit)
}

// RestoreClaudeWebSearchResults fills assistant web_search_tool_result blocks
// whose content is missing or empty with the content stored for sessionKey.
// Blocks that already carry content, and blocks without a stored entry, are
// left unchanged.
func RestoreClaudeWebSearchResults(body []byte, sessionKey string, ttl ...time.Duration) []byte {
	return RestoreClaudeWebSearchResultsContext(context.Background(), body, sessionKey, ttl...)
}

// RestoreClaudeWebSearchResultsContext fills assistant web_search_tool_result
// blocks whose content is missing or empty with the content stored for
// sessionKey, and drops web_search_result_location citations from assistant
// turns whose web_search_tool_result remains empty.
func RestoreClaudeWebSearchResultsContext(ctx context.Context, body []byte, sessionKey string, ttl ...time.Duration) []byte {
	if !bytes.Contains(body, claudeWebSearchToolResultMarker) {
		return body
	}
	trimmedSessionKey := strings.TrimSpace(sessionKey)
	if trimmedSessionKey == "" && !bytes.Contains(body, claudeWebSearchCitationMarker) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	restored := 0
	droppedCitations := 0
	turnHasEmptyWebSearchResult := false
	for i, message := range messages.Array() {
		if message.Get("role").String() != "assistant" {
			if claudeUserMessageIsTurnBoundary(message) {
				turnHasEmptyWebSearchResult = false
			}
			continue
		}
		blocks := message.Get("content").Array()
		for j, block := range blocks {
			if block.Get("type").String() != "web_search_tool_result" || !claudeWebSearchResultContentEmpty(block.Get("content")) {
				continue
			}
			if trimmedSessionKey != "" {
				cached, ok, errGet := cache.GetClaudeWebSearchResultRequired(ctx, trimmedSessionKey, block.Get("tool_use_id").String(), ttl...)
				if errGet == nil && ok && len(cached) > 0 {
					updated, errSet := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content.%d.content", i, j), cached)
					if errSet == nil {
						body = updated
						restored++
						continue
					}
					log.Debugf("claude web search results: restore messages.%d.content.%d failed: %v", i, j, errSet)
				}
			}
			turnHasEmptyWebSearchResult = true
		}
		if !turnHasEmptyWebSearchResult {
			continue
		}
		for j, block := range blocks {
			if block.Get("type").String() != "text" {
				continue
			}
			var dropped int
			body, dropped = stripClaudeWebSearchCitations(body, fmt.Sprintf("messages.%d.content.%d", i, j), block.Get("citations"))
			droppedCitations += dropped
		}
	}
	if restored > 0 {
		log.Debugf("claude web search results: restored %d result block(s) from cache", restored)
	}
	if droppedCitations > 0 {
		log.Debugf("claude web search results: dropped %d web_search_result_location citation(s) from assistant turn without search results", droppedCitations)
	}
	return body
}

func claudeUserMessageIsTurnBoundary(message gjson.Result) bool {
	content := message.Get("content")
	if !content.IsArray() {
		return true
	}
	for _, block := range content.Array() {
		if block.Get("type").String() != "tool_result" {
			return true
		}
	}
	return false
}

func stripClaudeWebSearchCitations(body []byte, blockPath string, citations gjson.Result) ([]byte, int) {
	if !citations.Exists() || !citations.IsArray() {
		return body, 0
	}
	var kept [][]byte
	dropped := 0
	citations.ForEach(func(_, citation gjson.Result) bool {
		if citation.Get("type").String() == "web_search_result_location" {
			dropped++
			return true
		}
		kept = append(kept, []byte(citation.Raw))
		return true
	})
	if dropped == 0 {
		return body, 0
	}
	citationsPath := blockPath + ".citations"
	if len(kept) == 0 {
		updated, errDelete := sjson.DeleteBytes(body, citationsPath)
		if errDelete != nil {
			return body, 0
		}
		return updated, dropped
	}
	updated, errSet := sjson.SetRawBytes(body, citationsPath, translatorcommon.JoinRawArray(kept))
	if errSet != nil {
		return body, 0
	}
	return updated, dropped
}

func claudeWebSearchResultContentEmpty(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	return content.IsArray() && len(content.Array()) == 0
}
