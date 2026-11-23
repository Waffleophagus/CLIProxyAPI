package helps

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// ClaudeAdvisorOwnerTTL bounds cached advisor block credential ownership.
	ClaudeAdvisorOwnerTTL   = 24 * time.Hour
	claudeAdvisorOwnerLimit = 4096
)

type claudeAdvisorOwnerEntry struct {
	key       string
	owner     string
	expiresAt time.Time
	elem      *list.Element
}

// ClaudeAdvisorOwnerStore tracks which Claude credential minted each advisor
// tool_use_id so foreign encrypted advisor blocks can be stripped when a
// conversation moves to a different credential.
type ClaudeAdvisorOwnerStore struct {
	mu      sync.Mutex
	entries map[string]*claudeAdvisorOwnerEntry
	order   *list.List
	nowFunc func() time.Time
}

// NewClaudeAdvisorOwnerStore allocates a bounded in-memory advisor owner store.
func NewClaudeAdvisorOwnerStore() *ClaudeAdvisorOwnerStore {
	return &ClaudeAdvisorOwnerStore{
		entries: make(map[string]*claudeAdvisorOwnerEntry),
		order:   list.New(),
	}
}

func (s *ClaudeAdvisorOwnerStore) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func claudeAdvisorOwnerKey(scope, toolUseID string) string {
	scopeSum := sha256.Sum256([]byte(scope))
	idSum := sha256.Sum256([]byte(toolUseID))
	return hex.EncodeToString(scopeSum[:]) + ":" + hex.EncodeToString(idSum[:])
}

func (s *ClaudeAdvisorOwnerStore) initLocked() {
	if s.entries == nil {
		s.entries = make(map[string]*claudeAdvisorOwnerEntry)
	}
	if s.order == nil {
		s.order = list.New()
	}
}

// Lookup returns the recorded owning credential identity for toolUseID and
// slides the retention window forward for active sessions.
func (s *ClaudeAdvisorOwnerStore) Lookup(scope, toolUseID string) (string, bool) {
	if s == nil || scope == "" || toolUseID == "" {
		return "", false
	}
	key := claudeAdvisorOwnerKey(scope, toolUseID)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		return "", false
	}
	entry, found := s.entries[key]
	if !found {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		if s.order != nil && entry.elem != nil {
			s.order.Remove(entry.elem)
		}
		delete(s.entries, key)
		return "", false
	}
	entry.expiresAt = now.Add(ClaudeAdvisorOwnerTTL)
	if s.order != nil && entry.elem != nil {
		s.order.MoveToBack(entry.elem)
	}
	return entry.owner, true
}

// Remember records owner for toolUseID using first-writer-wins semantics.
func (s *ClaudeAdvisorOwnerStore) Remember(scope, toolUseID, owner string) {
	if s == nil || scope == "" || toolUseID == "" || owner == "" {
		return
	}
	key := claudeAdvisorOwnerKey(scope, toolUseID)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if entry, exists := s.entries[key]; exists {
		if now.Before(entry.expiresAt) {
			return
		}
		entry.owner = strings.Clone(owner)
		entry.expiresAt = now.Add(ClaudeAdvisorOwnerTTL)
		s.order.MoveToBack(entry.elem)
		return
	}
	entry := &claudeAdvisorOwnerEntry{
		key:       key,
		owner:     strings.Clone(owner),
		expiresAt: now.Add(ClaudeAdvisorOwnerTTL),
	}
	entry.elem = s.order.PushBack(entry)
	s.entries[key] = entry
	for s.order.Len() > claudeAdvisorOwnerLimit {
		front := s.order.Front()
		if front == nil {
			break
		}
		oldest := front.Value.(*claudeAdvisorOwnerEntry)
		s.order.Remove(front)
		delete(s.entries, oldest.key)
	}
}

// ClaudeAdvisorRequestState carries per-request advisor ownership context so
// unknown request IDs and newly minted response IDs are recorded only after
// upstream acceptance.
type ClaudeAdvisorRequestState struct {
	store      *ClaudeAdvisorOwnerStore
	scope      string
	owner      string
	unknownIDs []string
	accepted   bool
}

// FilterUnknownIDsFromFinalBody performs a read-only check against the final
// outbound request payload (after user payload rules) and drops any unknown
// advisor IDs that are no longer present in body.messages.
func (r *ClaudeAdvisorRequestState) FilterUnknownIDsFromFinalBody(body []byte) {
	if r == nil || len(r.unknownIDs) == 0 {
		return
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		r.unknownIDs = nil
		return
	}
	present := make(map[string]struct{}, len(r.unknownIDs))
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if content.IsArray() {
			for _, block := range content.Array() {
				if id := claudeAdvisorBlockID(block); id != "" {
					present[id] = struct{}{}
				}
			}
		} else if content.IsObject() {
			blockType := content.Get("type").String()
			if blockType == "advisor_tool_result" || blockType == "advisor_redacted_result" {
				if id := strings.TrimSpace(content.Get("tool_use_id").String()); id != "" {
					present[id] = struct{}{}
				}
			}
		}
	}
	filtered := r.unknownIDs[:0]
	for _, id := range r.unknownIDs {
		if _, ok := present[id]; ok {
			filtered = append(filtered, id)
		}
	}
	r.unknownIDs = filtered
}

// RecordAccepted commits ownership of any previously unknown advisor IDs seen
// in the request after the upstream credential accepted the request.
func (r *ClaudeAdvisorRequestState) RecordAccepted() {
	if r == nil || r.store == nil || r.accepted {
		return
	}
	r.accepted = true
	for _, id := range r.unknownIDs {
		r.store.Remember(r.scope, id, r.owner)
	}
}

// RecordResponseJSON records advisor blocks minted in a non-streaming Claude
// Messages JSON response.
func (r *ClaudeAdvisorRequestState) RecordResponseJSON(data []byte) {
	if r == nil || r.store == nil || len(data) == 0 {
		return
	}
	content := gjson.GetBytes(data, "content")
	if !content.IsArray() {
		return
	}
	for _, block := range content.Array() {
		if id := claudeAdvisorBlockID(block); id != "" {
			r.store.Remember(r.scope, id, r.owner)
		}
	}
}

// ObserveSSELine inspects one SSE line from a Claude stream, committing
// request-side unknown advisor IDs on message_start and recording newly minted
// advisor blocks on content_block_start.
func (r *ClaudeAdvisorRequestState) ObserveSSELine(line []byte) {
	if r == nil || r.store == nil || len(line) == 0 {
		return
	}
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if !bytes.Contains(payload, []byte("content_block_start")) && !bytes.Contains(payload, []byte("message_start")) {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start":
		r.RecordAccepted()
	case "content_block_start":
		if id := claudeAdvisorBlockID(root.Get("content_block")); id != "" {
			r.store.Remember(r.scope, id, r.owner)
		}
	}
}

// RecordResponseSSE records advisor blocks minted across a buffered Claude SSE
// response body.
func (r *ClaudeAdvisorRequestState) RecordResponseSSE(data []byte) {
	if r == nil || r.store == nil || len(data) == 0 {
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		r.ObserveSSELine(line)
	}
}

// StripForeignAdvisorBlocks removes advisor state blocks owned by another
// credential from body.messages and returns the updated body, per-request
// tracking state and the number of stripped blocks.
func (s *ClaudeAdvisorOwnerStore) StripForeignAdvisorBlocks(body []byte, scope, owner string) ([]byte, *ClaudeAdvisorRequestState, int) {
	if s == nil || len(body) == 0 || scope == "" || owner == "" {
		return body, nil, 0
	}
	state := &ClaudeAdvisorRequestState{
		store: s,
		scope: scope,
		owner: owner,
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || !claudeMessagesContainAdvisorState(messages) {
		return body, state, 0
	}

	messageResults := messages.Array()
	ordinaryToolUseIDs := make(map[string]struct{})
	for _, msg := range messageResults {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if block.Get("type").String() == "tool_use" {
				if id := strings.TrimSpace(block.Get("id").String()); id != "" {
					ordinaryToolUseIDs[id] = struct{}{}
				}
			}
		}
	}

	resolvedOwners := make(map[string]string)
	unknownSeen := make(map[string]struct{})
	isForeign := func(id string) bool {
		if id == "" {
			return false
		}
		recorded, checked := resolvedOwners[id]
		if !checked {
			if foundOwner, ok := s.Lookup(scope, id); ok {
				recorded = foundOwner
			}
			resolvedOwners[id] = recorded
		}
		if recorded == "" {
			if _, seen := unknownSeen[id]; !seen {
				unknownSeen[id] = struct{}{}
				state.unknownIDs = append(state.unknownIDs, strings.Clone(id))
			}
			return false
		}
		return recorded != owner
	}

	strippedBlocks := 0
	var droppedMessages []int

	for i, msg := range messageResults {
		content := msg.Get("content")
		if content.IsArray() {
			contentResults := content.Array()
			var droppedParts []int

			for j, block := range contentResults {
				blockType := block.Get("type").String()
				switch blockType {
				case "server_tool_use":
					if block.Get("name").String() != "advisor" {
						continue
					}
					if isForeign(strings.TrimSpace(block.Get("id").String())) {
						droppedParts = append(droppedParts, j)
						strippedBlocks++
					}
				case "advisor_tool_result", "advisor_redacted_result":
					if isForeign(strings.TrimSpace(block.Get("tool_use_id").String())) {
						droppedParts = append(droppedParts, j)
						strippedBlocks++
					}
				case "tool_result":
					inner := block.Get("content")
					if inner.IsObject() && inner.Get("type").String() == "advisor_redacted_result" {
						id := strings.TrimSpace(block.Get("tool_use_id").String())
						if isForeign(id) {
							if _, isOrdinary := ordinaryToolUseIDs[id]; isOrdinary {
								body, _ = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content.%d.content", i, j), "")
							} else {
								droppedParts = append(droppedParts, j)
							}
							strippedBlocks++
						}
					} else if inner.IsArray() {
						innerItems := inner.Array()
						var advisorSubIndices []int
						for subIdx, sub := range innerItems {
							if sub.Get("type").String() == "advisor_redacted_result" {
								advisorSubIndices = append(advisorSubIndices, subIdx)
							}
						}
						if len(advisorSubIndices) == 0 {
							continue
						}
						id := strings.TrimSpace(block.Get("tool_use_id").String())
						if !isForeign(id) {
							continue
						}
						_, isOrdinary := ordinaryToolUseIDs[id]
						if !isOrdinary {
							// The paired server_tool_use(name="advisor") is dropped in the
							// same pass, so drop the entire tool_result to avoid a dangling
							// tool_use_id reference.
							droppedParts = append(droppedParts, j)
							strippedBlocks++
						} else if len(advisorSubIndices) < len(innerItems) {
							for k := len(advisorSubIndices) - 1; k >= 0; k-- {
								body, _ = sjson.DeleteBytes(body, fmt.Sprintf("messages.%d.content.%d.content.%d", i, j, advisorSubIndices[k]))
							}
							strippedBlocks += len(advisorSubIndices)
						} else {
							body, _ = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content.%d.content", i, j), "")
							strippedBlocks += len(advisorSubIndices)
						}
					}
				}
			}

			if len(droppedParts) > 0 {
				if len(droppedParts) == len(contentResults) {
					droppedMessages = append(droppedMessages, i)
					continue
				}
				for k := len(droppedParts) - 1; k >= 0; k-- {
					body, _ = sjson.DeleteBytes(body, fmt.Sprintf("messages.%d.content.%d", i, droppedParts[k]))
				}
			}
		} else if content.IsObject() {
			blockType := content.Get("type").String()
			if blockType == "advisor_tool_result" || blockType == "advisor_redacted_result" {
				if isForeign(strings.TrimSpace(content.Get("tool_use_id").String())) {
					droppedMessages = append(droppedMessages, i)
					strippedBlocks++
				}
			}
		}
	}

	for k := len(droppedMessages) - 1; k >= 0; k-- {
		body, _ = sjson.DeleteBytes(body, fmt.Sprintf("messages.%d", droppedMessages[k]))
	}
	return body, state, strippedBlocks
}

func claudeAdvisorBlockID(block gjson.Result) string {
	if !block.IsObject() {
		return ""
	}
	switch block.Get("type").String() {
	case "server_tool_use":
		if block.Get("name").String() == "advisor" {
			return strings.TrimSpace(block.Get("id").String())
		}
	case "advisor_tool_result", "advisor_redacted_result":
		return strings.TrimSpace(block.Get("tool_use_id").String())
	case "tool_result":
		if claudeToolResultContainsAdvisorRedacted(block.Get("content")) {
			return strings.TrimSpace(block.Get("tool_use_id").String())
		}
	}
	return ""
}

func claudeToolResultContainsAdvisorRedacted(inner gjson.Result) bool {
	if inner.IsObject() {
		return inner.Get("type").String() == "advisor_redacted_result"
	}
	if inner.IsArray() {
		for _, sub := range inner.Array() {
			if sub.Get("type").String() == "advisor_redacted_result" {
				return true
			}
		}
	}
	return false
}

func claudeMessagesContainAdvisorState(messages gjson.Result) bool {
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if content.IsArray() {
			for _, block := range content.Array() {
				if claudeAdvisorBlockID(block) != "" {
					return true
				}
			}
		} else if content.IsObject() {
			blockType := content.Get("type").String()
			if blockType == "advisor_tool_result" || blockType == "advisor_redacted_result" {
				if strings.TrimSpace(content.Get("tool_use_id").String()) != "" {
					return true
				}
			}
		}
	}
	return false
}
