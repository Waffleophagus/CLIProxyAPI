package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKimiAndKimiAIOAuthRoutes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	for _, provider := range []string{"kimi", "kimi-ai"} {
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider="+provider, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected %s login route: %d", provider, w.Code)
		}
	}

	registered := false
	for _, route := range server.engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v8/management/oauth/auth-url" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("OAuth login route not registered")
	}
}
