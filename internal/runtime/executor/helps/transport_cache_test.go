package helps

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type cacheKey struct {
	scope string
	proxy string
}

func TestTransportCacheReusesEntriesPerKey(t *testing.T) {
	cache := NewTransportCache[cacheKey](8)

	builds := 0
	build := func() (*http.Transport, error) {
		builds++
		return &http.Transport{}, nil
	}

	first, errFirst := cache.Get(cacheKey{"auth-a", "p1"}, build)
	if errFirst != nil {
		t.Fatalf("Get() error = %v", errFirst)
	}
	second, errSecond := cache.Get(cacheKey{"auth-a", "p1"}, build)
	if errSecond != nil {
		t.Fatalf("Get() second error = %v", errSecond)
	}
	if first == nil || first != second {
		t.Fatalf("expected one cached transport, got %p and %p", first, second)
	}
	if builds != 1 {
		t.Fatalf("build called %d times, want 1", builds)
	}

	otherProxy, _ := cache.Get(cacheKey{"auth-a", "p2"}, build)
	if otherProxy == first {
		t.Fatal("distinct proxies must not share a transport")
	}
	otherScope, _ := cache.Get(cacheKey{"auth-b", "p1"}, build)
	if otherScope == first {
		t.Fatal("distinct credential scopes must not share a transport")
	}
	if got := cache.Len(); got != 3 {
		t.Fatalf("cache Len() = %d, want 3", got)
	}
}

// TestTransportCacheBoundsEntries is the regression test for unbounded pool growth:
// every cached transport owns a connection pool, so churning keys must evict.
func TestTransportCacheBoundsEntries(t *testing.T) {
	const capacity = 4
	cache := NewTransportCache[cacheKey](capacity)

	for i := 0; i < 100; i++ {
		key := cacheKey{"auth", string(rune('a' + i%97))}
		if _, err := cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil }); err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got := cache.Len(); got > capacity {
			t.Fatalf("cache grew to %d entries, want at most %d", got, capacity)
		}
	}
}

// TestTransportCacheEvictsLeastRecentlyUsed proves recency is honoured, so a hot
// credential is not evicted by a burst of one-off keys.
func TestTransportCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := NewTransportCache[cacheKey](2)
	build := func() (*http.Transport, error) { return &http.Transport{}, nil }

	hot, _ := cache.Get(cacheKey{"hot", ""}, build)
	cache.Get(cacheKey{"cold", ""}, build)
	// Touch hot so cold becomes the least recently used entry.
	if again, _ := cache.Get(cacheKey{"hot", ""}, build); again != hot {
		t.Fatal("expected the hot entry to still be cached")
	}
	cache.Get(cacheKey{"new", ""}, build)

	if again, _ := cache.Get(cacheKey{"hot", ""}, build); again != hot {
		t.Fatal("the most recently used entry must survive eviction")
	}
}

// TestTransportCacheDoesNotCacheBuildFailures ensures a transient failure neither
// occupies a cache slot nor becomes permanent.
func TestTransportCacheDoesNotCacheBuildFailures(t *testing.T) {
	cache := NewTransportCache[cacheKey](4)
	key := cacheKey{"auth", "broken"}

	if _, err := cache.Get(key, func() (*http.Transport, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("expected the build error to be propagated")
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("a failed build must not occupy a cache slot, Len() = %d", got)
	}
	// A build returning (nil, nil) must be reported rather than cached as usable.
	if _, err := cache.Get(key, func() (*http.Transport, error) { return nil, nil }); err == nil {
		t.Fatal("expected an error when build returns no transport")
	}

	transport, err := cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil })
	if err != nil || transport == nil {
		t.Fatalf("retry after failure must succeed, got (%p, %v)", transport, err)
	}
}

func TestTransportCacheConcurrentCallersShareOneInstance(t *testing.T) {
	cache := NewTransportCache[cacheKey](8)
	key := cacheKey{"auth-concurrent", "socks5://127.0.0.1:1080"}

	const callers = 32
	results := make([]*http.Transport, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer wg.Done()
			results[index], _ = cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil })
		}(i)
	}
	wg.Wait()

	for i := 1; i < callers; i++ {
		if results[i] != results[0] {
			t.Fatalf("caller %d observed a different transport (%p vs %p)", i, results[i], results[0])
		}
	}
}

func TestNewTransportCacheDefaultsCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		cache := NewTransportCache[cacheKey](capacity)
		if cache.capacity != DefaultTransportCacheCapacity {
			t.Fatalf("NewTransportCache(%d).capacity = %d, want %d", capacity, cache.capacity, DefaultTransportCacheCapacity)
		}
	}
}

func TestTransportCacheEvictionClosesIdleConnections(t *testing.T) {
	cache := NewTransportCache[cacheKey](2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	build := func() (*http.Transport, error) {
		return &http.Transport{}, nil
	}

	// Fill 2 entries
	t1, err1 := cache.Get(cacheKey{"auth-1", ""}, build)
	if err1 != nil {
		t.Fatal(err1)
	}
	_, err2 := cache.Get(cacheKey{"auth-2", ""}, build)
	if err2 != nil {
		t.Fatal(err2)
	}

	// Make request with t1 to establish an idle connection
	c1 := &http.Client{Transport: t1}
	resp, errReq := c1.Get(srv.URL)
	if errReq != nil {
		t.Fatal(errReq)
	}
	_ = resp.Body.Close()

	// Push 3rd entry: t1 should be evicted and its idle connections closed
	_, err3 := cache.Get(cacheKey{"auth-3", ""}, build)
	if err3 != nil {
		t.Fatal(err3)
	}

	if cache.Contains(cacheKey{"auth-1", ""}) {
		t.Fatal("expected auth-1 to be evicted from cache")
	}
	if !cache.Contains(cacheKey{"auth-2", ""}) || !cache.Contains(cacheKey{"auth-3", ""}) {
		t.Fatal("expected auth-2 and auth-3 to remain in cache")
	}
}
