package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestIssue5190ModelDetailLocalVisibility(t *testing.T) {
	server := newTestServer(t)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(t.Name(), "openai", []*registry.ModelInfo{{ID: "local/only", Object: "model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(t.Name()) })
	get := func(id string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+id, nil)
		if authorized {
			req.Header.Set("Authorization", "Bearer test-key")
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		return rr
	}
	if rr := get("local/only", true); rr.Code != http.StatusOK {
		t.Fatalf("local detail: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("local/only", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized detail: %d", rr.Code)
	}
}
