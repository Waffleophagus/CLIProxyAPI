package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	// ClaudeThinkingReplayCacheTTL limits how long signed assistant turns stay replayable.
	ClaudeThinkingReplayCacheTTL = 1 * time.Hour

	// ClaudeThinkingReplayCacheMaxEntries bounds process memory used by Claude replay continuity.
	ClaudeThinkingReplayCacheMaxEntries = 10240

	// ClaudeThinkingReplayCacheEvictBatchSize leaves headroom after reaching capacity.
	ClaudeThinkingReplayCacheEvictBatchSize = 128

	// ClaudeThinkingReplayCacheMaxBytesPerSession bounds all cached assistant turns for one session.
	ClaudeThinkingReplayCacheMaxBytesPerSession = 8 << 20

	// ClaudeThinkingReplayCacheMaxTurnsPerSession bounds the number of assistant turns per session.
	ClaudeThinkingReplayCacheMaxTurnsPerSession = 64

	// ClaudeThinkingReplayCacheMaxBlocksPerTurn prevents pathological content arrays.
	ClaudeThinkingReplayCacheMaxBlocksPerTurn = 512

	// ClaudeThinkingReplayCacheMaxTotalBytes bounds aggregate in-process Claude replay content.
	ClaudeThinkingReplayCacheMaxTotalBytes = 256 << 20
)

type claudeThinkingReplayEntry struct {
	Contents   [][]byte
	Timestamp  time.Time
	Generation string
	Deleted    bool
}

// ClaudeThinkingReplaySnapshot identifies the exact replay generation read for one request.
type ClaudeThinkingReplaySnapshot struct {
	generation string
	loaded     bool
	found      bool
}

var (
	claudeThinkingReplayMu         sync.Mutex
	claudeThinkingReplayEntries    = make(map[string]claudeThinkingReplayEntry)
	claudeThinkingReplayTotalBytes int
)

// CacheClaudeThinkingReplayBestEffort stores one complete signed assistant content array.
func CacheClaudeThinkingReplayBestEffort(ctx context.Context, modelFamily, sessionKey string, content []byte) bool {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validClaudeThinkingReplayContent(content) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	contents := [][]byte{append([]byte(nil), content...)}
	generation := uuid.NewString()

	storeClaudeThinkingReplayLocal(key, contents, generation, false, time.Now())
	return true
}

// GetClaudeThinkingReplayRequired retrieves all cached assistant turns for request-time replay.
func GetClaudeThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) ([][]byte, bool, error) {
	contents, _, found, errGet := GetClaudeThinkingReplayWithSnapshotRequired(ctx, modelFamily, sessionKey)
	return contents, found, errGet
}

// GetClaudeThinkingReplayWithSnapshotRequired retrieves replay content and the exact cache state read.
func GetClaudeThinkingReplayWithSnapshotRequired(ctx context.Context, modelFamily, sessionKey string) ([][]byte, ClaudeThinkingReplaySnapshot, bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil, ClaudeThinkingReplaySnapshot{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, ok := claudeThinkingReplayEntries[key]
	if !ok || now.Sub(entry.Timestamp) > ClaudeThinkingReplayCacheTTL {
		if ok {
			claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
			delete(claudeThinkingReplayEntries, key)
		}
		entry = reserveClaudeThinkingReplayLocalLocked(key, now)
	}
	entry.Timestamp = now
	claudeThinkingReplayEntries[key] = entry
	snapshot := ClaudeThinkingReplaySnapshot{generation: entry.Generation, loaded: true, found: true}
	if entry.Deleted {
		return nil, snapshot, false, nil
	}
	return cloneClaudeThinkingReplayContents(entry.Contents), snapshot, len(entry.Contents) > 0, nil
}

// ReplaceClaudeThinkingReplayIfUnchanged appends a completed assistant turn only if the request snapshot is current.
func ReplaceClaudeThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot ClaudeThinkingReplaySnapshot, content []byte) (bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validClaudeThinkingReplayContent(content) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, sessionKey, content), nil
	}

	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, found := claudeThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	contents := appendClaudeThinkingReplayContent(entry.Contents, content)
	claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
	claudeThinkingReplayTotalBytes += claudeThinkingReplayEntryBytes(contents)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{
		Contents:   contents,
		Timestamp:  time.Now(),
		Generation: uuid.NewString(),
	}
	enforceClaudeThinkingReplayLimitsLocked()
	return true, nil
}

// DeleteClaudeThinkingReplayIfUnchanged clears replay state only if the request snapshot is current.
func DeleteClaudeThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot ClaudeThinkingReplaySnapshot) (bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return true, DeleteClaudeThinkingReplayRequired(ctx, modelFamily, sessionKey)
	}
	generation := uuid.NewString()

	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, found := claudeThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{Timestamp: time.Now(), Generation: generation, Deleted: true}
	return true, nil
}

// DeleteClaudeThinkingReplayRequired removes stale replay state unconditionally.
func DeleteClaudeThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) error {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	claudeThinkingReplayMu.Lock()
	if entry, found := claudeThinkingReplayEntries[key]; found {
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
		delete(claudeThinkingReplayEntries, key)
	}
	claudeThinkingReplayMu.Unlock()
	return nil
}

// ClearClaudeThinkingReplayCache clears only Claude replay state.
func ClearClaudeThinkingReplayCache() {
	claudeThinkingReplayMu.Lock()
	claudeThinkingReplayEntries = make(map[string]claudeThinkingReplayEntry)
	claudeThinkingReplayTotalBytes = 0
	claudeThinkingReplayMu.Unlock()
}

func reserveClaudeThinkingReplayLocalLocked(key string, now time.Time) claudeThinkingReplayEntry {
	entry := claudeThinkingReplayEntry{Timestamp: now, Generation: uuid.NewString(), Deleted: true}
	claudeThinkingReplayEntries[key] = entry
	enforceClaudeThinkingReplayLimitsLocked()
	return entry
}

func storeClaudeThinkingReplayLocal(key string, contents [][]byte, generation string, deleted bool, now time.Time) {
	cacheCleanupOnce.Do(startCacheCleanup)
	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	if previous, found := claudeThinkingReplayEntries[key]; found {
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(previous.Contents)
	}
	cloned := cloneClaudeThinkingReplayContents(contents)
	claudeThinkingReplayTotalBytes += claudeThinkingReplayEntryBytes(cloned)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{Contents: cloned, Timestamp: now, Generation: generation, Deleted: deleted}
	enforceClaudeThinkingReplayLimitsLocked()
}

func appendClaudeThinkingReplayContent(contents [][]byte, content []byte) [][]byte {
	cloned := cloneClaudeThinkingReplayContents(contents)
	for _, existing := range cloned {
		if claudeThinkingReplayJSONEqual(existing, content) {
			return cloned
		}
	}
	cloned = append(cloned, append([]byte(nil), content...))
	for len(cloned) > ClaudeThinkingReplayCacheMaxTurnsPerSession || claudeThinkingReplayEntryBytes(cloned) > ClaudeThinkingReplayCacheMaxBytesPerSession {
		if len(cloned) == 0 {
			break
		}
		cloned = cloned[1:]
	}
	return cloned
}

func cloneClaudeThinkingReplayContents(contents [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(contents))
	for _, content := range contents {
		cloned = append(cloned, append([]byte(nil), content...))
	}
	return cloned
}

func claudeThinkingReplayEntryBytes(contents [][]byte) int {
	total := 0
	for _, content := range contents {
		total += len(content)
	}
	return total
}

func claudeThinkingReplayCacheKey(modelFamily, sessionKey string) string {
	modelFamily = strings.TrimSpace(modelFamily)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelFamily == "" || sessionKey == "" {
		return ""
	}
	return strings.Join([]string{"claude-thinking-replay", modelFamily, sessionKey}, "\x00")
}

func validClaudeThinkingReplayContent(content []byte) bool {
	if len(content) == 0 || len(content) > ClaudeThinkingReplayCacheMaxBytesPerSession || !gjson.ValidBytes(content) {
		return false
	}
	root := gjson.ParseBytes(content)
	return root.IsArray() && len(root.Array()) > 0 && len(root.Array()) <= ClaudeThinkingReplayCacheMaxBlocksPerTurn
}

func claudeThinkingReplayJSONEqual(left, right []byte) bool {
	leftCanonical, leftOK := claudeThinkingReplayCanonicalJSON(left)
	rightCanonical, rightOK := claudeThinkingReplayCanonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(leftCanonical, rightCanonical)
}

func claudeThinkingReplayCanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	canonical, errMarshal := json.Marshal(value)
	return canonical, errMarshal == nil
}

func enforceClaudeThinkingReplayLimitsLocked() {
	for len(claudeThinkingReplayEntries) > ClaudeThinkingReplayCacheMaxEntries || claudeThinkingReplayTotalBytes > ClaudeThinkingReplayCacheMaxTotalBytes {
		if len(claudeThinkingReplayEntries) == 0 {
			claudeThinkingReplayTotalBytes = 0
			return
		}
		evictOldestClaudeThinkingReplayEntriesLocked(ClaudeThinkingReplayCacheEvictBatchSize)
	}
}

func evictOldestClaudeThinkingReplayEntriesLocked(count int) {
	if count <= 0 || len(claudeThinkingReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(claudeThinkingReplayEntries))
	for key, entry := range claudeThinkingReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		entry := claudeThinkingReplayEntries[candidates[i].key]
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
		delete(claudeThinkingReplayEntries, candidates[i].key)
	}
}

func purgeExpiredClaudeThinkingReplayCache(now time.Time) {
	claudeThinkingReplayMu.Lock()
	for key, entry := range claudeThinkingReplayEntries {
		if now.Sub(entry.Timestamp) > ClaudeThinkingReplayCacheTTL {
			claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
			delete(claudeThinkingReplayEntries, key)
		}
	}
	claudeThinkingReplayMu.Unlock()
}
