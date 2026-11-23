package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexSearchReadErrorDiagnosticInDownstreamLog(t *testing.T) {
	for _, fullLog := range []bool{false, true} {
		name := "deferred"
		if fullLog {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			server.cfg.RequestLog = fullLog
			credential := &auth.Auth{
				ID:       "codex-search-diagnostic-" + name,
				Provider: "codex",
				Status:   auth.StatusActive,
				Metadata: map[string]any{"access_token": "codex-token"},
			}
			if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
				t.Fatalf("register Codex auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "gpt-5-codex"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
			server.handlers.AuthManager.RegisterExecutor(&codexSearchCaptureExecutor{
				statuses:     []int{http.StatusOK},
				responseBody: &errorSearchResponseBody{payload: []byte(`{"partial":true}`)},
			})
			logsDir := t.TempDir()
			router := gin.New()
			router.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(fullLog, logsDir, "", 10)))
			router.POST("/v1/alpha/search", server.codexAlphaSearch)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5-codex","query":"test"}`)))
			if recorder.Code != http.StatusBadGateway || recorder.Body.String() != `{"error":"Failed to read Codex search response"}` {
				t.Fatalf("client response changed: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
			if errGlob != nil || len(paths) != 1 {
				t.Fatalf("logs = %v, error = %v", paths, errGlob)
			}
			data, errRead := os.ReadFile(paths[0])
			if errRead != nil {
				t.Fatal(errRead)
			}
			before, response, found := strings.Cut(string(data), "=== RESPONSE ===\n")
			if !found || !strings.Contains(response, "Error: "+io.ErrUnexpectedEOF.Error()+"\n\nHeaders:\n") || !strings.Contains(response, "\nBody:\n"+recorder.Body.String()) {
				t.Fatalf("response diagnostic missing:\n%s", data)
			}
			if strings.Contains(before, io.ErrUnexpectedEOF.Error()) || !strings.Contains(before, `{"partial":true}`) {
				t.Fatalf("upstream response was modified:\n%s", data)
			}
		})
	}
}

type errorSearchResponseBody struct {
	payload []byte
	read    atomic.Bool
	closed  atomic.Bool
}

func (b *errorSearchResponseBody) Read(p []byte) (int, error) {
	if !b.read.CompareAndSwap(false, true) {
		return 0, io.EOF
	}
	return copy(p, b.payload), io.ErrUnexpectedEOF
}

func (b *errorSearchResponseBody) Close() error {
	b.closed.Store(true)
	return nil
}
