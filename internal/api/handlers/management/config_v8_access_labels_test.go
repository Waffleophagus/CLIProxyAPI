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

func TestConfigV8AccessAPIKeyLabelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("access:\n  api-keys:\n    - sk-one # first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.GET("/v8/management/config/*path", h.ConfigV8)

	body := `{"api-keys":["sk-one","sk-two"],"api-key-labels":{"0a1b2c3d":"opencode"}}`
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v8/management/config/access", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("save: status=%d body=%s", response.Code, response.Body.String())
	}
	if got := h.cfg.APIKeyLabels["0a1b2c3d"]; got != "opencode" || len(h.cfg.APIKeys) != 2 {
		t.Fatalf("runtime config not updated: keys=%v labels=%v", h.cfg.APIKeys, h.cfg.APIKeyLabels)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v8/management/config/access/api-key-labels", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"0a1b2c3d":"opencode"}` {
		t.Fatalf("labels not readable: status=%d body=%s", response.Code, response.Body.String())
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "# first") {
		t.Errorf("key comment lost on save:\n%s", saved)
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.APIKeyLabels["0a1b2c3d"] != "opencode" {
		t.Errorf("labels not persisted: %v\n%s", reloaded.APIKeyLabels, saved)
	}
}
