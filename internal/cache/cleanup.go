package cache

import (
	"sync"
	"time"
)

// CacheCleanupInterval controls how often stale entries are purged.
const CacheCleanupInterval = 10 * time.Minute

// cacheCleanupOnce ensures the background cleanup goroutine starts only once.
var cacheCleanupOnce sync.Once

// startCacheCleanup launches a background goroutine that periodically
// removes expired entries from the replay caches.
func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(CacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCaches()
		}
	}()
}

// purgeExpiredCaches removes expired entries from every replay cache.
func purgeExpiredCaches() {
	now := time.Now()
	purgeExpiredCodexReasoningReplayCache(now)
	purgeExpiredXAIReasoningReplayCache(now)
	purgeExpiredClaudeThinkingReplayCache(now)
	purgeExpiredClaudeWebSearchResultCache(now)
}
