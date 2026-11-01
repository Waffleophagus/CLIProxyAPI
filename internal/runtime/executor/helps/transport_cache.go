package helps

import (
	"container/list"
	"errors"
	"net/http"
	"sync"
)

// DefaultTransportCacheCapacity bounds how many transports a TransportCache keeps
// alive at once. Every cached transport owns an independent connection pool, so an
// unbounded cache would let idle sockets and the goroutines managing them grow
// without limit whenever keys churn, for example when a credential's proxy is
// rotated through the management API or when an SDK embedder supplies a freshly
// built base transport per request.
const DefaultTransportCacheCapacity = 64

// TransportCache memoizes HTTP transports under a comparable key using a bounded
// LRU. Evicting an entry closes its idle connections so neither the pool nor its
// background goroutines outlive the cache entry.
//
// The key type is generic so callers can mix value identity (a normalized proxy
// URL) with pointer identity (a base transport supplied by the caller) without the
// cache retaining either beyond the LRU window.
type TransportCache[K comparable] struct {
	mu       sync.Mutex
	capacity int
	// order keeps the most recently used entry at the front.
	order *list.List
	items map[K]*list.Element
}

type transportCacheEntry[K comparable] struct {
	key       K
	transport *http.Transport
}

// NewTransportCache returns a cache holding at most capacity transports. A
// non-positive capacity falls back to DefaultTransportCacheCapacity.
func NewTransportCache[K comparable](capacity int) *TransportCache[K] {
	if capacity <= 0 {
		capacity = DefaultTransportCacheCapacity
	}
	return &TransportCache[K]{
		capacity: capacity,
		order:    list.New(),
		items:    make(map[K]*list.Element, capacity),
	}
}

// Get returns the transport cached under key, calling build on the first use of
// that key. Concurrent callers observe the same instance.
//
// A build error is propagated without being cached, so a later call can retry and
// a failed lookup never occupies a cache slot. build must not call back into the
// same cache.
func (c *TransportCache[K]) Get(key K, build func() (*http.Transport, error)) (*http.Transport, error) {
	if c == nil {
		return nil, errors.New("transport cache: nil cache")
	}
	if build == nil {
		return nil, errors.New("transport cache: nil build function")
	}

	c.mu.Lock()
	locked := true
	defer func() {
		if locked {
			c.mu.Unlock()
		}
	}()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		transport := element.Value.(*transportCacheEntry[K]).transport
		return transport, nil
	}

	transport, errBuild := build()
	if errBuild != nil {
		return nil, errBuild
	}
	if transport == nil {
		return nil, errors.New("transport cache: build returned no transport")
	}

	// Double check in case key was populated while build was running
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		existing := element.Value.(*transportCacheEntry[K]).transport
		locked = false
		c.mu.Unlock()
		transport.CloseIdleConnections()
		return existing, nil
	}

	c.items[key] = c.order.PushFront(&transportCacheEntry[K]{key: key, transport: transport})
	evicted := c.evictLocked()
	locked = false
	c.mu.Unlock()

	for _, t := range evicted {
		t.CloseIdleConnections()
	}
	return transport, nil
}

// evictLocked drops least recently used entries until the cache fits its capacity,
// returning a slice of evicted transports to be closed outside the lock.
// Closing idle connections is what actually releases the evicted pool; in-flight
// requests still holding the transport are unaffected because CloseIdleConnections
// only reaps connections that are currently idle.
func (c *TransportCache[K]) evictLocked() []*http.Transport {
	var evicted []*http.Transport
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		entry := oldest.Value.(*transportCacheEntry[K])
		delete(c.items, entry.key)
		evicted = append(evicted, entry.transport)
	}
	return evicted
}

// Len reports how many transports the cache currently holds.
func (c *TransportCache[K]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Contains reports whether key exists in the cache.
func (c *TransportCache[K]) Contains(key K) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[key]
	return ok
}
