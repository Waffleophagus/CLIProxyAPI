package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type refreshRecordExecutor struct {
	provider   string
	refreshCnt atomic.Int32
}

func (e *refreshRecordExecutor) Identifier() string {
	return e.provider
}

func (e *refreshRecordExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshCnt.Add(1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	auth.Metadata["refresh_token"] = "refresh-token"
	auth.Metadata["expires_in"] = int64(3600)
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func (e *refreshRecordExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *refreshRecordExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthFiles_AllAndSpecific(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	fileA := filepath.Join(authDir, "acme-1.json")
	fileB := filepath.Join(authDir, "acme-2.json")
	_ = os.WriteFile(fileA, []byte(`{"type":"acme","refresh_token":"ref-1","access_token":"old-1"}`), 0o600)
	_ = os.WriteFile(fileB, []byte(`{"type":"acme","refresh_token":"ref-2","access_token":"old-2"}`), 0o600)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "acme"}
	manager.RegisterExecutor(exec)

	auth1 := &coreauth.Auth{
		ID:       "acme-1.json",
		Provider: "acme",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "acme", "refresh_token": "ref-1", "access_token": "old-1"},
	}
	auth2 := &coreauth.Auth{
		ID:          "acme-2.json",
		Provider:    "acme",
		Status:      coreauth.StatusError,
		Unavailable: true,
		LastError:   &coreauth.Error{Message: "unauthorized"},
		Metadata:    map[string]any{"type": "acme", "refresh_token": "ref-2", "access_token": "old-2"},
	}
	_, _ = manager.Register(context.Background(), auth1)
	_, _ = manager.Register(context.Background(), auth2)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	engine := gin.New()
	engine.POST("/auth-files/refresh", h.RefreshAuthFiles)

	// 1. Refresh all
	req := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?all=true", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected ok=true, got %v", resp)
	}

	// Wait briefly for refresh to execute
	time.Sleep(50 * time.Millisecond)

	if cnt := exec.refreshCnt.Load(); cnt < 2 {
		t.Fatalf("expected at least 2 refreshes, got %d", cnt)
	}

	// 2. Auth2 was in StatusError, now should be active/recovering
	a2, exists := manager.GetByID("acme-2.json")
	if !exists || a2.Status == coreauth.StatusError {
		t.Fatalf("expected auth2 status to be recovered from error, got %+v", a2)
	}

	// 3. Refresh single file by name
	prevCnt := exec.refreshCnt.Load()
	reqSingle := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=acme-1.json", nil)
	wSingle := httptest.NewRecorder()
	engine.ServeHTTP(wSingle, reqSingle)

	if wSingle.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wSingle.Code, wSingle.Body.String())
	}
	if newCnt := exec.refreshCnt.Load(); newCnt != prevCnt+1 {
		t.Fatalf("expected cnt to increment by 1, was %d now %d", prevCnt, newCnt)
	}

	// 4. Refresh nonexistent file
	reqMissing := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=nonexistent.json", nil)
	wMissing := httptest.NewRecorder()
	engine.ServeHTTP(wMissing, reqMissing)

	if wMissing.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", wMissing.Code, wMissing.Body.String())
	}

	// 5. Refresh via chunked JSON request body (ContentLength = -1)
	chunkedBody := strings.NewReader(`{"name":"acme-1.json"}`)
	reqChunked := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", chunkedBody)
	reqChunked.Header.Set("Content-Type", "application/json")
	reqChunked.TransferEncoding = []string{"chunked"}
	reqChunked.ContentLength = -1
	wChunked := httptest.NewRecorder()
	engine.ServeHTTP(wChunked, reqChunked)

	if wChunked.Code != http.StatusOK {
		t.Fatalf("expected chunked request status 200, got %d: %s", wChunked.Code, wChunked.Body.String())
	}

	// 6. Malformed JSON request body returns 400
	reqBadJSON := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", strings.NewReader(`{invalid`))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	wBadJSON := httptest.NewRecorder()
	engine.ServeHTTP(wBadJSON, reqBadJSON)

	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed JSON to return 400, got %d", wBadJSON.Code)
	}
}

// The refresh response must describe the credential without any token,
// API key or proxy secret.
func TestRefreshAuthFiles_ResponseOmitsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	path := filepath.Join(authDir, "acme-secret.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"acme","refresh_token":"ref-secret-1","access_token":"old-secret-1"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&refreshRecordExecutor{provider: "acme"})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "acme-secret.json",
		FileName: "acme-secret.json",
		Provider: "acme",
		Label:    "Acme work",
		Status:   coreauth.StatusActive,
		ProxyURL: "http://proxyuser:proxy-pass-secret@127.0.0.1:3128",
		Attributes: map[string]string{
			"path":    path,
			"api_key": "sk-attr-secret-key-123456",
		},
		Metadata: map[string]any{
			"type":          "acme",
			"email":         "dev@example.com",
			"refresh_token": "ref-secret-1",
			"access_token":  "old-secret-1",
			"id_token":      "id-secret-token",
		},
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	engine := gin.New()
	engine.POST("/credentials/refresh", h.RefreshAuthFiles)

	req := httptest.NewRequest(http.MethodPost, "/credentials/refresh", strings.NewReader(`{"name":"acme-secret.json"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{
		"refreshed-token", "refresh-token", "ref-secret-1", "old-secret-1", "id-secret-token",
		"sk-attr-secret-key-123456", "proxy-pass-secret",
		"access_token", "refresh_token", "id_token", "metadata", "attributes", "proxy_url",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("refresh response leaks %q: %s", secret, body)
		}
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var resp struct {
		OK   bool           `json:"ok"`
		Auth map[string]any `json:"auth"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &resp); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if !resp.OK || resp.Auth["id"] != "acme-secret.json" || resp.Auth["provider"] != "acme" || resp.Auth["email"] != "dev@example.com" || resp.Auth["label"] != "Acme work" {
		t.Fatalf("unexpected summary: %s", body)
	}
	if _, ok := resp.Auth["expires_at"]; !ok {
		t.Fatalf("summary lacks expires_at: %s", body)
	}
	if _, ok := resp.Auth["last_refresh"]; !ok {
		t.Fatalf("summary lacks last_refresh: %s", body)
	}
}

// API-key credentials expose the key as their account; the list masks it.
func TestListAuthFiles_MasksAPIKeyAccount(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "claude-key",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"api_key":      "sk-ant-list-secret-998877",
		},
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-ant-list-secret-998877") {
		t.Fatalf("list leaks the API key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "8877") {
		t.Fatalf("list lacks the masked account: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
