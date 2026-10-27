package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUploadAuthFile_PreservesPriorityAttributes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{"type":"codex","email":"midai0530@gmail.com","priority":98}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-midai0530@gmail.com-plus.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}

	auth, ok := manager.GetByID("codex-midai0530@gmail.com-plus.json")
	if !ok || auth == nil {
		t.Fatalf("expected uploaded auth record to exist")
	}
	if got := auth.Attributes["priority"]; got != "98" {
		t.Fatalf("priority attribute = %q, want %q", got, "98")
	}
	if got := auth.Metadata["priority"]; got != float64(98) {
		t.Fatalf("priority metadata = %#v, want 98", got)
	}
}

func TestUploadAuthFile_InvokesPostAuthPersistHook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	var hookedAuth *coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		if auth != nil {
			hookedAuth = auth.Clone()
		}
		return nil
	})

	content := `{"type":"codex","email":"user@example.com","access_token":"token-123"}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-user@example.com.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if hookedAuth == nil {
		t.Fatal("expected postAuthPersistHook to be invoked on auth file upload")
	}
	if hookedAuth.ID != "codex-user@example.com.json" {
		t.Fatalf("hooked auth ID = %q, want codex-user@example.com.json", hookedAuth.ID)
	}
}

func TestUploadAuthFile_RejectsRemovedProviders(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	// Mixed case and surrounding whitespace must be rejected too.
	for _, provider := range []string{"gemini", "gemini-cli", "antigravity", "vertex", "aistudio", "GeMiNi-ClI", " vertex ", "kimi", "kimi-ai", "kimi.ai", "kimi.com", "devin"} {
		for _, multipartUpload := range []bool{false, true} {
			authDir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

			name := strings.ToLower(strings.TrimSpace(provider)) + "-user.json"
			content := []byte(`{"type":"` + provider + `","email":"user@example.com"}`)

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			var req *http.Request
			if multipartUpload {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				part, errCreate := writer.CreateFormFile("file", name)
				if errCreate != nil {
					t.Fatalf("failed to create multipart file: %v", errCreate)
				}
				if _, errWrite := part.Write(content); errWrite != nil {
					t.Fatalf("failed to write multipart content: %v", errWrite)
				}
				if errClose := writer.Close(); errClose != nil {
					t.Fatalf("failed to close multipart writer: %v", errClose)
				}
				req = httptest.NewRequest(http.MethodPost, "/v8/management/credentials", &body)
				req.Header.Set("Content-Type", writer.FormDataContentType())
			} else {
				req = httptest.NewRequest(http.MethodPost, "/v8/management/credentials?name="+name, bytes.NewReader(content))
				req.Header.Set("Content-Type", "application/json")
			}
			ctx.Request = req

			h.UploadAuthFile(ctx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s (multipart=%v): expected status %d, got %d with body %s", provider, multipartUpload, http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if _, ok := manager.GetByID(name); ok {
				t.Fatalf("%s (multipart=%v): removed provider credential was registered", provider, multipartUpload)
			}
			if _, errStat := os.Stat(filepath.Join(authDir, name)); !os.IsNotExist(errStat) {
				t.Fatalf("%s (multipart=%v): removed provider credential was persisted (stat err: %v)", provider, multipartUpload, errStat)
			}
		}
	}
}
