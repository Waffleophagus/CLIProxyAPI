package management

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func mustHash(t *testing.T, secret string) string {
	t.Helper()
	hash, errHash := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	return string(hash)
}

// newCountingHandler returns a handler with a configured bcrypt hash, a fake
// clock and a counter of real bcrypt comparisons.
func newCountingHandler(t *testing.T, secret string) (*Handler, *atomic.Int32, *fakeClock) {
	t.Helper()
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = mustHash(t, secret)
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	compares := &atomic.Int32{}
	h := &Handler{cfg: cfg, failedAttempts: make(map[string]*attemptInfo)}
	h.verifiedKeys.now = clock.Now
	h.compareKeyHash = func(hash, password []byte) error {
		compares.Add(1)
		return bcrypt.CompareHashAndPassword(hash, password)
	}
	return h, compares, clock
}

func mustAuth(t *testing.T, h *Handler, clientIP string, local bool, key string) {
	t.Helper()
	if allowed, status, msg := h.AuthenticateManagementKey(clientIP, local, key); !allowed {
		t.Fatalf("auth denied: status=%d msg=%q", status, msg)
	}
}

func TestManagementKeyCacheHitSkipsBcrypt(t *testing.T) {
	h, compares, _ := newCountingHandler(t, "secret")
	for i := 0; i < 5; i++ {
		mustAuth(t, h, "127.0.0.1", true, "secret")
	}
	if got := compares.Load(); got != 1 {
		t.Fatalf("bcrypt comparisons = %d, want 1", got)
	}
}

func TestManagementKeyCacheInvalidatedBySecretChange(t *testing.T) {
	h, compares, _ := newCountingHandler(t, "secret")
	mustAuth(t, h, "127.0.0.1", true, "secret")

	h.mu.Lock()
	h.cfg.RemoteManagement.SecretKey = mustHash(t, "rotated")
	h.mu.Unlock()
	if allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret"); allowed || status != http.StatusUnauthorized {
		t.Fatalf("old key after rotation: allowed=%t status=%d, want 401", allowed, status)
	}
	mustAuth(t, h, "127.0.0.1", true, "rotated")
	if got := compares.Load(); got != 3 {
		t.Fatalf("bcrypt comparisons = %d, want 3 (rotation must not reuse cached results)", got)
	}

	// Re-hashing the same secret (new salt) is a different configured hash.
	h.mu.Lock()
	h.cfg.RemoteManagement.SecretKey = mustHash(t, "rotated")
	h.mu.Unlock()
	mustAuth(t, h, "127.0.0.1", true, "rotated")
	if got := compares.Load(); got != 4 {
		t.Fatalf("bcrypt comparisons = %d, want 4", got)
	}
}

func TestManagementKeyCacheExpires(t *testing.T) {
	h, compares, clock := newCountingHandler(t, "secret")
	mustAuth(t, h, "127.0.0.1", true, "secret")
	clock.now = clock.now.Add(verifiedKeyCacheTTL - time.Second)
	mustAuth(t, h, "127.0.0.1", true, "secret")
	if got := compares.Load(); got != 1 {
		t.Fatalf("bcrypt comparisons within TTL = %d, want 1", got)
	}
	clock.now = clock.now.Add(2 * time.Second)
	mustAuth(t, h, "127.0.0.1", true, "secret")
	if got := compares.Load(); got != 2 {
		t.Fatalf("bcrypt comparisons after TTL = %d, want 2", got)
	}
}

func TestManagementKeyCacheNeverCachesFailuresAndBansStillApply(t *testing.T) {
	h, compares, _ := newCountingHandler(t, "secret")
	mustAuth(t, h, "127.0.0.1", true, "secret") // cached success

	for i := 0; i < 5; i++ {
		allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong")
		if allowed || status != http.StatusUnauthorized {
			t.Fatalf("wrong key attempt %d: allowed=%t status=%d", i+1, allowed, status)
		}
	}
	if got := compares.Load(); got != 6 {
		t.Fatalf("bcrypt comparisons = %d, want 6 (every failure re-checked)", got)
	}
	// The ban applies even though the correct key is cached.
	if allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret"); allowed || status != http.StatusForbidden {
		t.Fatalf("cached key while banned: allowed=%t status=%d, want 403", allowed, status)
	}
}

func TestManagementKeyCacheStillEnforcesAllowRemote(t *testing.T) {
	h, _, _ := newCountingHandler(t, "secret")
	mustAuth(t, h, "127.0.0.1", true, "secret")
	if allowed, status, _ := h.AuthenticateManagementKey("192.168.1.20", false, "secret"); allowed || status != http.StatusForbidden {
		t.Fatalf("remote with cached key: allowed=%t status=%d, want 403", allowed, status)
	}
}

func TestVerifiedKeyCacheIsBounded(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := &verifiedKeyCache{now: clock.Now}
	first := verifiedKeyDigest("hash", "key-0")
	c.add(first)
	for i := 1; i <= verifiedKeyCacheMax; i++ {
		c.add(verifiedKeyDigest("hash", "key-"+string(rune('a'+i))))
	}
	if len(c.entries) != verifiedKeyCacheMax {
		t.Fatalf("entries = %d, want %d", len(c.entries), verifiedKeyCacheMax)
	}
	if c.contains(first) {
		t.Fatal("oldest entry was not evicted")
	}
	if !c.contains(verifiedKeyDigest("hash", "key-"+string(rune('a'+verifiedKeyCacheMax)))) {
		t.Fatal("newest entry missing")
	}
	if c.contains(verifiedKeyDigest("other-hash", "key-"+string(rune('a'+verifiedKeyCacheMax)))) {
		t.Fatal("digest not bound to the configured hash")
	}
}
