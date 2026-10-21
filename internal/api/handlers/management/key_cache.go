package management

import (
	"crypto/sha256"
	"crypto/subtle"
	"sync"
	"time"
)

const (
	// verifiedKeyCacheTTL bounds how long a successful bcrypt verification is
	// reused before the key is checked against the hash again.
	verifiedKeyCacheTTL = 5 * time.Minute
	// verifiedKeyCacheMax bounds the number of remembered verifications.
	verifiedKeyCacheMax = 16
)

// verifiedKeyCache remembers recent successful management key verifications
// so a dashboard polling several endpoints does not pay for a bcrypt
// comparison on every request. Entries are digests bound to the configured
// hash, so changing the secret invalidates them. Failures are never cached.
// The zero value is ready to use.
type verifiedKeyCache struct {
	mu      sync.Mutex
	entries []verifiedKeyEntry
	// now overrides time.Now in tests.
	now func() time.Time
}

type verifiedKeyEntry struct {
	digest  [sha256.Size]byte
	expires time.Time
}

// verifiedKeyDigest binds the presented key to the configured hash.
func verifiedKeyDigest(secretHash, provided string) [sha256.Size]byte {
	hashID := sha256.Sum256([]byte(secretHash))
	keyID := sha256.Sum256([]byte(provided))
	return sha256.Sum256(append(hashID[:], keyID[:]...))
}

func (c *verifiedKeyCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// pruneLocked drops expired entries.
func (c *verifiedKeyCache) pruneLocked(now time.Time) {
	kept := c.entries[:0]
	for _, entry := range c.entries {
		if now.Before(entry.expires) {
			kept = append(kept, entry)
		}
	}
	c.entries = kept
}

// contains reports whether digest was verified within the TTL. It compares
// against every entry in constant time.
func (c *verifiedKeyCache) contains(digest [sha256.Size]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(c.clock())
	found := 0
	for i := range c.entries {
		found |= subtle.ConstantTimeCompare(c.entries[i].digest[:], digest[:])
	}
	return found == 1
}

// add records a successful verification, evicting the oldest entry when full.
func (c *verifiedKeyCache) add(digest [sha256.Size]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.pruneLocked(now)
	for i := range c.entries {
		if subtle.ConstantTimeCompare(c.entries[i].digest[:], digest[:]) == 1 {
			c.entries = append(c.entries[:i], c.entries[i+1:]...)
			break
		}
	}
	if len(c.entries) >= verifiedKeyCacheMax {
		c.entries = append(c.entries[:0], c.entries[len(c.entries)-verifiedKeyCacheMax+1:]...)
	}
	c.entries = append(c.entries, verifiedKeyEntry{digest: digest, expires: now.Add(verifiedKeyCacheTTL)})
}
