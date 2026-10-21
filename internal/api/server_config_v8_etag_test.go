package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestConfigV8ETagFlowWithoutCORS covers the GET -> If-Match flow through the
// real server. The management API is same-origin only, so even requests that
// carry an Origin header get no CORS headers.
func TestConfigV8ETagFlowWithoutCORS(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	if err := os.WriteFile(server.configFilePath, []byte("config-version: 8\nserver: {port: 8317}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := proxyconfig.LoadConfig(server.configFilePath)
	if err != nil {
		t.Fatal(err)
	}
	server.cfgMu.Lock()
	server.cfg = cfg
	server.cfgMu.Unlock()
	server.mgmt.SetConfig(cfg)

	do := func(method, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v8/management/config.yaml", strings.NewReader(body))
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		req.Header.Set("Authorization", "Bearer test-management-key")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		return recorder
	}
	wantETag := func(recorder *httptest.ResponseRecorder, status int) string {
		t.Helper()
		if recorder.Code != status {
			t.Fatalf("status = %d, want %d body=%s", recorder.Code, status, recorder.Body.String())
		}
		for name := range recorder.Header() {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Fatalf("management response has CORS header %s", name)
			}
		}
		etag := recorder.Header().Get("ETag")
		if etag == "" {
			t.Fatal("response is missing ETag")
		}
		return etag
	}

	etag := wantETag(do(http.MethodGet, "", ""), http.StatusOK)
	// A stale precondition reports the current version.
	wantETag(do(http.MethodPut, "config-version: 8\n", `"stale"`), http.StatusPreconditionFailed)
	// A successful conditional write chains through its new ETag.
	wantETag(do(http.MethodPut, "config-version: 8\nserver: {port: 8318}\n", etag), http.StatusOK)
}
