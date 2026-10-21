package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/slugs"
)

type slugTestTicker struct{ c chan time.Time }

func (t slugTestTicker) C() <-chan time.Time { return t.c }
func (t slugTestTicker) Stop()               {}

func TestRoutingSlugsEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var mu sync.Mutex
	served := []string{"GLM-5.3-Flash-EXL3"}
	checked := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	resolver := slugs.NewManager(slugs.Options{
		Fetch: func(context.Context, slugs.Target) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), served...), nil
		},
		NewTicker: func(time.Duration) slugs.Ticker { return slugTestTicker{c: make(chan time.Time)} },
		Now:       func() time.Time { return checked },
	})
	resolver.Reconfigure(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{
			Name:           "dgx-spark",
			BaseURL:        "http://172.16.1.181:8888/v1",
			FollowInterval: "45s",
			APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "sk-secret-never-listed"}},
			Models:         []config.OpenAICompatibilityModel{{Alias: "DGX-SPARK", Follow: true}},
		},
		{
			Name:    "openrouter",
			BaseURL: "https://openrouter.example/v1",
			Models:  []config.OpenAICompatibilityModel{{Name: "vendor/model", Alias: "router-model"}},
		},
	}})
	resolver.Start(context.Background())
	defer resolver.Stop()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.SetSlugResolver(resolver)

	call := func(method, target, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			ginCtx.Request.Header.Set("Content-Type", "application/json")
		}
		handler(ginCtx)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) []slugs.Status {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "sk-secret") {
			t.Fatalf("slug response leaked an API key: %s", rec.Body.String())
		}
		var payload struct {
			Slugs []slugs.Status `json:"slugs"`
		}
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
			t.Fatal(errDecode)
		}
		return payload.Slugs
	}

	// Refresh waits for the poll, so the follow entry is resolved afterwards.
	list := decode(call(http.MethodPost, "/v8/management/routing/slugs/refresh", "", h.RefreshSlugs))
	if len(list) != 2 {
		t.Fatalf("slugs = %+v", list)
	}
	follow := list[0]
	if follow.Group != "dgx-spark" || follow.Alias != "DGX-SPARK" || follow.Mode != slugs.ModeFollow ||
		follow.ResolvedModel != "GLM-5.3-Flash-EXL3" || follow.BaseURL != "http://172.16.1.181:8888/v1" ||
		follow.PollInterval != "45s" || follow.LastSuccessAt == nil || !follow.LastSuccessAt.Equal(checked) ||
		len(follow.ReportedModels) != 1 || follow.LastError != "" {
		t.Fatalf("follow slug = %+v", follow)
	}
	if pinned := list[1]; pinned.Mode != slugs.ModePinned || pinned.ResolvedModel != "vendor/model" || pinned.LastCheckAt != nil {
		t.Fatalf("pinned slug = %+v", pinned)
	}

	mu.Lock()
	served = []string{"GLM-5.3-Flash-NVFP4"}
	mu.Unlock()
	list = decode(call(http.MethodPost, "/v8/management/routing/slugs/refresh", `{"group":"dgx-spark"}`, h.RefreshSlugs))
	if list[0].ResolvedModel != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("resolved after refresh = %q", list[0].ResolvedModel)
	}
	if got := decode(call(http.MethodGet, "/v8/management/routing/slugs", "", h.GetSlugs)); got[0].ResolvedModel != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("GET slugs = %+v", got)
	}

	if rec := call(http.MethodPost, "/v8/management/routing/slugs/refresh?group=missing", "", h.RefreshSlugs); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown group status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPost, "/v8/management/routing/slugs/refresh", `{"group":`, h.RefreshSlugs); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d", rec.Code)
	}
}
