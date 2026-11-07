package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeOAuthV8CallbackPreservesURLSafeState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, form := range []string{"json", "redirect_url", "query"} {
		t.Run(form, func(t *testing.T) {
			authDir := t.TempDir()
			handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.POST("/v8/management/oauth/callback", handler.PostOAuthCallbackV8)
			router.GET("/v8/management/oauth/callback", handler.GetOAuthCallbackV8)
			state := strings.Repeat("Ab9-_", 8) + "AbA"
			RegisterOAuthSession(state, "anthropic")
			defer CancelOAuthSession(state)

			callbackRequest := func(callbackState string) *http.Request {
				values := map[string]string{"provider": "claude", "code": "callback-code", "state": callbackState}
				query := url.Values{}
				for key, value := range values {
					query.Set(key, value)
				}
				if form == "query" {
					return httptest.NewRequest(http.MethodGet, "/v8/management/oauth/callback?"+query.Encode(), nil)
				}
				if form == "redirect_url" {
					values = map[string]string{"redirect_url": "http://localhost:54545/callback?" + query.Encode()}
				}
				doc := []byte(`{}`)
				for key, value := range values {
					var errSet error
					if doc, errSet = sjson.SetBytes(doc, key, value); errSet != nil {
						t.Fatalf("set callback field %s: %v", key, errSet)
					}
				}
				request := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/callback", bytes.NewReader(doc))
				request.Header.Set("Content-Type", "application/json")
				return request
			}

			wrongState := httptest.NewRecorder()
			router.ServeHTTP(wrongState, callbackRequest(strings.ToLower(state)))
			if wrongState.Code != http.StatusNotFound {
				t.Fatalf("wrong-state callback status = %d, want 404: %s", wrongState.Code, wrongState.Body.String())
			}
			entries, errReadDir := os.ReadDir(authDir)
			if errReadDir != nil || len(entries) != 0 {
				t.Fatalf("wrong-state callback wrote a file: entries = %v, error = %v", entries, errReadDir)
			}

			response := httptest.NewRecorder()
			router.ServeHTTP(response, callbackRequest(state))
			if response.Code != http.StatusOK {
				t.Fatalf("callback status = %d, want 200: %s", response.Code, response.Body.String())
			}
			payload, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-anthropic-"+state+".oauth"))
			if errRead != nil {
				t.Fatalf("read callback file: %v", errRead)
			}
			if got := gjson.GetBytes(payload, "state").String(); got != state {
				t.Fatalf("callback file state = %q, want %q", got, state)
			}
			if got := gjson.GetBytes(payload, "code").String(); got != "callback-code" {
				t.Fatalf("callback file code = %q, want callback-code", got)
			}
		})
	}
}
