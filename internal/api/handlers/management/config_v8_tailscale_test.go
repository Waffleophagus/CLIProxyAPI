package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8JSONRedactsTailscaleAuthKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("config-version: 8\ntailscale:\n  enabled: true\n  auth-key: tskey-auth-test-secret\n  management-users: [matt@github]\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	r := gin.New()
	r.GET("/v8/management/config", h.ConfigV8)
	r.PUT("/v8/management/config", h.ConfigV8)
	r.GET("/v8/management/config.yaml", h.ConfigV8)
	r.GET("/v8/management/config/*path", h.ConfigV8)
	r.PUT("/v8/management/config/*path", h.ConfigV8)
	r.PATCH("/v8/management/config/*path", h.ConfigV8)

	authKey := func() string {
		loaded, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		return loaded.Tailscale.AuthKey
	}
	do := func(method, url, body string) string {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, url, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", method, url, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	// JSON reads never contain the key; writing the read back keeps it.
	for _, url := range []string{"/v8/management/config", "/v8/management/config/tailscale"} {
		body := do(http.MethodGet, url, "")
		if strings.Contains(body, "tskey-auth-test-secret") || strings.Contains(body, "auth-key") {
			t.Fatalf("JSON read exposed the auth key: %s", body)
		}
		do(http.MethodPut, url, body)
		if got := authKey(); got != "tskey-auth-test-secret" {
			t.Fatalf("after PUT %s auth key = %q", url, got)
		}
	}

	do(http.MethodPatch, "/v8/management/config/tailscale", `{"hostname":"proxy"}`)
	if got := authKey(); got != "tskey-auth-test-secret" {
		t.Fatalf("after PATCH auth key = %q", got)
	}

	// The YAML export is the raw file and keeps the key, like other secrets.
	if body := do(http.MethodGet, "/v8/management/config.yaml", ""); !strings.Contains(body, "tskey-auth-test-secret") {
		t.Fatal("YAML export lost the auth key")
	}

	// An explicit value replaces it; an explicit empty string clears it.
	do(http.MethodPut, "/v8/management/config/tailscale", `{"enabled":true,"auth-key":"tskey-auth-new"}`)
	if got := authKey(); got != "tskey-auth-new" {
		t.Fatalf("after replace auth key = %q", got)
	}
	do(http.MethodPut, "/v8/management/config/tailscale", `{"enabled":true,"auth-key":""}`)
	if got := authKey(); got != "" {
		t.Fatalf("after clear auth key = %q", got)
	}
}
