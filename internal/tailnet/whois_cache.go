package tailnet

import (
	"context"
	"sync"
	"time"
)

// DefaultWhoIsCacheTTL bounds how long a WhoIs answer is reused per remote address.
const DefaultWhoIsCacheTTL = 30 * time.Second

// maxWhoIsCacheEntries caps the cache; expired entries are purged when full.
const maxWhoIsCacheEntries = 1024

// WhoIsFunc resolves the tailnet identity behind a remote "ip:port" address.
type WhoIsFunc func(ctx context.Context, remoteAddr string) (Peer, error)

type whoIsEntry struct {
	peer    Peer
	expires time.Time
}

// WhoIsCache memoizes successful WhoIs lookups per remote address for a short
// TTL; errors are not cached.
type WhoIsCache struct {
	lookup WhoIsFunc
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]whoIsEntry
}

// NewWhoIsCache wraps lookup. A nil now uses time.Now; ttl <= 0 uses the default.
func NewWhoIsCache(lookup WhoIsFunc, ttl time.Duration, now func() time.Time) *WhoIsCache {
	if ttl <= 0 {
		ttl = DefaultWhoIsCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &WhoIsCache{lookup: lookup, ttl: ttl, now: now, entries: make(map[string]whoIsEntry)}
}

// WhoIs returns the cached identity for remoteAddr or performs a lookup.
func (c *WhoIsCache) WhoIs(ctx context.Context, remoteAddr string) (Peer, error) {
	now := c.now()
	c.mu.Lock()
	if entry, ok := c.entries[remoteAddr]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return entry.peer, nil
	}
	c.mu.Unlock()

	peer, errLookup := c.lookup(ctx, remoteAddr)
	if errLookup != nil {
		return Peer{}, errLookup
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxWhoIsCacheEntries {
		for addr, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, addr)
			}
		}
		if len(c.entries) >= maxWhoIsCacheEntries {
			c.entries = make(map[string]whoIsEntry)
		}
	}
	c.entries[remoteAddr] = whoIsEntry{peer: peer, expires: now.Add(c.ttl)}
	return peer, nil
}
