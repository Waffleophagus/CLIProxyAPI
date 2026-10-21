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

// Replacing a whole list through the JSON API keeps the comments of entries
// that survive (matched by value, alias or name) and drops those of removed
// entries.
func TestConfigV8ListReplacementKeepsSurvivorComments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := `config-version: 8
access:
  api-keys:
    # LAPTOP HEAD
    - "sk-laptop-0000000001" # LAPTOP INLINE
    - "sk-ci-00000000000002" # CI INLINE
api-keys:
  openai-compatibility:
    # GROUP HEAD
    - name: "dgx" # GROUP NAME INLINE
      base-url: "http://127.0.0.1:18799/v1" # BASE INLINE
      keys: []
      models:
        # MAIN HEAD
        - alias: "MAIN" # MAIN INLINE
          follow: true
        - alias: "CODER"
          name: "qwen-coder" # CODER MODEL INLINE
`
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	send := func(method, url, body string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", method, url, rec.Code, rec.Body.String())
		}
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return string(data)
	}
	expect := func(data string, present, absent []string) {
		t.Helper()
		for _, marker := range present {
			if n := strings.Count(data, marker); n != 1 {
				t.Fatalf("%s appears %d times, want 1:\n%s", marker, n, data)
			}
		}
		for _, marker := range absent {
			if strings.Contains(data, marker) {
				t.Fatalf("%s should be gone with its entry:\n%s", marker, data)
			}
		}
	}

	// Add a key in front and revoke the laptop key: the CI key keeps its comment.
	data := send(http.MethodPut, "/v8/management/config/access/api-keys", `["sk-new-000000000003","sk-ci-00000000000002"]`)
	expect(data, []string{"CI INLINE", "GROUP HEAD", "MAIN HEAD"}, []string{"LAPTOP HEAD", "LAPTOP INLINE"})
	if !strings.Contains(data, `"sk-ci-00000000000002" # CI INLINE`) {
		t.Fatalf("CI comment moved off its entry:\n%s", data)
	}

	// Replace the endpoint list as the UI does: delete MAIN, edit the group's
	// base URL, add a new slug. Surviving group and CODER keep their comments.
	data = send(http.MethodPut, "/v8/management/config/api-keys/openai-compatibility", `[{"name":"dgx","base-url":"http://127.0.0.1:18800/v1","keys":[],"models":[{"alias":"CODER","name":"qwen-coder"},{"alias":"NEW","follow":true}]}]`)
	expect(data, []string{"GROUP HEAD", "GROUP NAME INLINE", "BASE INLINE", "CODER MODEL INLINE", "CI INLINE"}, []string{"MAIN HEAD", "MAIN INLINE"})
	if !strings.Contains(data, `name: "qwen-coder" # CODER MODEL INLINE`) {
		t.Fatalf("CODER comment moved off its entry:\n%s", data)
	}

	// A whole-config PATCH that replaces the list behaves the same.
	data = send(http.MethodPatch, "/v8/management/config", `{"access":{"api-keys":["sk-ci-00000000000002"]}}`)
	expect(data, []string{"CI INLINE", "GROUP HEAD", "CODER MODEL INLINE"}, nil)
}
