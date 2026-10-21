package helps

import (
	"context"
	"testing"
)

func resetSessionIDCache() {
	sessionIDCacheMu.Lock()
	sessionIDCache = make(map[string]sessionIDCacheEntry)
	sessionIDCacheMu.Unlock()
}

func TestCachedSessionIDRequiredUsesLocalMap(t *testing.T) {
	resetSessionIDCache()

	first, errFirst := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errFirst != nil {
		t.Fatalf("CachedSessionIDRequired() first error = %v", errFirst)
	}
	second, errSecond := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errSecond != nil {
		t.Fatalf("CachedSessionIDRequired() second error = %v", errSecond)
	}
	if first != second {
		t.Fatalf("session id = %q then %q, want local cache reuse", first, second)
	}
}
