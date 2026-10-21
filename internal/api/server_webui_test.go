package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func doRequest(s *Server, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.engine.ServeHTTP(rr, req)
	return rr
}

func TestWebUIServedWithoutAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	rr := doRequest(server, http.MethodGet, "/ui/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("/ui/ status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content type = %q", rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" || rr.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("missing security headers: %v", rr.Header())
	}
	// SPA routes fall back to index.html.
	if deep := doRequest(server, http.MethodGet, "/ui/usage", nil); deep.Code != http.StatusOK {
		t.Fatalf("/ui/usage status = %d", deep.Code)
	}
	// /ui redirects to the canonical trailing-slash path.
	if r := doRequest(server, http.MethodGet, "/ui", nil); r.Code != http.StatusMovedPermanently || r.Header().Get("Location") != "/ui/" {
		t.Fatalf("/ui: status=%d location=%q", r.Code, r.Header().Get("Location"))
	}
	// Data still needs the management key.
	if mgmt := doRequest(server, http.MethodGet, "/v8/management/config", nil); mgmt.Code != http.StatusUnauthorized {
		t.Fatalf("management without key status = %d", mgmt.Code)
	}
}

func TestRootRedirectsBrowsersOnly(t *testing.T) {
	server := newTestServer(t)

	browser := doRequest(server, http.MethodGet, "/", map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"})
	if browser.Code != http.StatusFound || browser.Header().Get("Location") != "/ui/" {
		t.Fatalf("browser: status=%d location=%q", browser.Code, browser.Header().Get("Location"))
	}
	for _, accept := range []string{"", "application/json", "*/*"} {
		rr := doRequest(server, http.MethodGet, "/", map[string]string{"Accept": accept})
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "CLI Proxy API Server") {
			t.Fatalf("accept %q: status=%d body=%s", accept, rr.Code, rr.Body.String())
		}
	}
}

func TestWebUIDisabledByConfig(t *testing.T) {
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}}}
	cfg.RemoteManagement.DisableUI = true
	server := newTestServerWithConfig(t, cfg)

	if rr := doRequest(server, http.MethodGet, "/ui/", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("/ui/ status = %d, want 404", rr.Code)
	}
	if rr := doRequest(server, http.MethodGet, "/ui", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("/ui status = %d, want 404", rr.Code)
	}
	rr := doRequest(server, http.MethodGet, "/", map[string]string{"Accept": "text/html"})
	if rr.Code != http.StatusOK {
		t.Fatalf("root with UI disabled: status = %d", rr.Code)
	}

	// Hot reload re-enables it.
	next := *server.cfg
	next.RemoteManagement.DisableUI = false
	server.UpdateClients(&next)
	if rr := doRequest(server, http.MethodGet, "/ui/", nil); rr.Code != http.StatusOK {
		t.Fatalf("/ui/ after enabling: status = %d", rr.Code)
	}
}

func TestSafeModeWarningLinksToWebUI(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServerWithOptions(t, WithExampleAPIKeySafeMode())
	cfg := *server.cfg
	cfg.APIKeys = []string{"your-api-key-1"}
	server.UpdateClients(&cfg)

	rr := doRequest(server, http.MethodGet, "/", map[string]string{"Accept": "text/html"})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `href="/ui/"`) {
		t.Fatalf("safe mode page: status=%d body=%s", rr.Code, rr.Body.String())
	}
	// The UI itself stays reachable in safe mode so the keys can be fixed.
	if ui := doRequest(server, http.MethodGet, "/ui/keys", nil); ui.Code != http.StatusOK {
		t.Fatalf("/ui/keys in safe mode: status = %d", ui.Code)
	}
}
