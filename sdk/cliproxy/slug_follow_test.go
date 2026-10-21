package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/slugs"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	"github.com/tidwall/gjson"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type idleTicker struct{ c chan time.Time }

func (t idleTicker) C() <-chan time.Time { return t.c }
func (t idleTicker) Stop()               {}

// followUpstream is an OpenAI-compatible server whose served model can be swapped.
type followUpstream struct {
	mu       sync.Mutex
	served   string
	received []string
	server   *httptest.Server
}

func newFollowUpstream(t *testing.T, served string) *followUpstream {
	t.Helper()
	u := &followUpstream{served: served}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		served := u.served
		u.mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + served + `","object":"model","owned_by":"tensorfold"}]}`))
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			model := gjson.GetBytes(body, "model").String()
			u.mu.Lock()
			u.received = append(u.received, model)
			u.mu.Unlock()
			if gjson.GetBytes(body, "stream").Bool() {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"`+model+`","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *followUpstream) serve(model string) {
	u.mu.Lock()
	u.served = model
	u.mu.Unlock()
}

func (u *followUpstream) lastReceived() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.received) == 0 {
		return ""
	}
	return u.received[len(u.received)-1]
}

type followHarness struct {
	service    *Service
	handler    http.Handler
	configPath string
	configRaw  []byte
}

func newFollowHarness(t *testing.T, upstreamURL string) *followHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousFactory := newSlugManager
	newSlugManager = func() *slugs.Manager {
		return slugs.NewManager(slugs.Options{NewTicker: func(time.Duration) slugs.Ticker { return idleTicker{c: make(chan time.Time)} }})
	}
	t.Cleanup(func() { newSlugManager = previousFactory })

	authDir := t.TempDir()
	configPath := filepath.Join(authDir, "config.yaml")
	raw := []byte("api-keys:\n  openai-compatibility:\n    - name: dgx-spark\n      base-url: " + upstreamURL + "/v1\n      keys: []\n      models:\n        - alias: DGX-SPARK\n          follow: true\n")
	if errWrite := os.WriteFile(configPath, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &config.Config{
		AuthDir:   authDir,
		SDKConfig: config.SDKConfig{APIKeys: []string{"slug-client-key"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "dgx-spark",
			BaseURL: upstreamURL + "/v1",
			Models:  []config.OpenAICompatibilityModel{{Alias: "DGX-SPARK", Follow: true}},
		}},
	}
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	// Keyless config auths reach the service through the watcher, which
	// synthesizes them from the config as loaded.
	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      service.currentSourceConfig(),
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil || len(auths) != 1 {
		t.Fatalf("synthesize auths: %d %v", len(auths), errSynthesize)
	}
	service.prepareCoreAuthForModelRegistration(ctx, auths[0])
	service.registerAvailableExecutors(ctx, executorRegistrationOptions{includeBaseline: true, auths: service.coreManager.List()})
	service.syncModelRuntime(ctx)
	t.Cleanup(func() {
		service.slugs.Stop()
		for _, auth := range service.coreManager.List() {
			if auth != nil {
				internalregistry.GetGlobalRegistry().UnregisterClient(auth.ID)
			}
		}
	})
	service.server = api.NewServer(service.currentSourceConfig(), service.coreManager, service.accessManager, configPath, service.serverOptions...)
	return &followHarness{service: service, handler: service.server.Handler(), configPath: configPath, configRaw: raw}
}

func (h *followHarness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer slug-client-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *followHarness) chat(t *testing.T, model string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"stream":   stream,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	return h.do(t, http.MethodPost, "/v1/chat/completions", string(body))
}

func assertStreamModel(t *testing.T, body []byte, want string) {
	t.Helper()
	chunks := 0
	for _, line := range bytes.Split(body, []byte("\n")) {
		payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		payload = bytes.TrimSpace(payload)
		if !ok || len(payload) == 0 || string(payload) == "[DONE]" {
			continue
		}
		chunks++
		if got := gjson.GetBytes(payload, "model").String(); got != want {
			t.Fatalf("stream chunk model = %q, want %q: %s", got, want, payload)
		}
	}
	if chunks == 0 {
		t.Fatalf("no stream chunks: %s", body)
	}
}

func TestFollowSlugRoutesToCurrentUpstreamModel(t *testing.T) {
	upstream := newFollowUpstream(t, "GLM-5.3-Flash-EXL3")
	h := newFollowHarness(t, upstream.server.URL)

	// Before the first resolve the slug is listed and fails with a clear error.
	models := h.do(t, http.MethodGet, "/v1/models", "")
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"DGX-SPARK"`) {
		t.Fatalf("models before resolve: %d %s", models.Code, models.Body.String())
	}
	pending := h.chat(t, "DGX-SPARK", false)
	if pending.Code != http.StatusServiceUnavailable || !strings.Contains(pending.Body.String(), "no resolved upstream model") {
		t.Fatalf("pending chat: %d %s", pending.Code, pending.Body.String())
	}
	if got := upstream.lastReceived(); got != "" {
		t.Fatalf("pending slug reached upstream with model %q", got)
	}

	ctx := context.Background()
	h.service.slugs.Start(ctx)
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	resp := h.chat(t, "DGX-SPARK", false)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat after resolve: %d %s", resp.Code, resp.Body.String())
	}
	if got := upstream.lastReceived(); got != "GLM-5.3-Flash-EXL3" {
		t.Fatalf("upstream model = %q, want GLM-5.3-Flash-EXL3", got)
	}
	if got := gjson.Get(resp.Body.String(), "model").String(); got != "DGX-SPARK" {
		t.Fatalf("client model = %q, want DGX-SPARK", got)
	}

	// Swap the served model; a deterministic re-poll must re-route the slug.
	upstream.serve("GLM-5.3-Flash-NVFP4")
	if errRefresh := h.service.slugs.Refresh(ctx, "dgx-spark"); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	resp = h.chat(t, "DGX-SPARK", false)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat after swap: %d %s", resp.Code, resp.Body.String())
	}
	if got := upstream.lastReceived(); got != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("upstream model after swap = %q, want GLM-5.3-Flash-NVFP4", got)
	}
	if got := gjson.Get(resp.Body.String(), "model").String(); got != "DGX-SPARK" {
		t.Fatalf("client model after swap = %q, want DGX-SPARK", got)
	}

	stream := h.chat(t, "DGX-SPARK", true)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream after swap: %d %s", stream.Code, stream.Body.String())
	}
	if got := upstream.lastReceived(); got != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("stream upstream model = %q", got)
	}
	assertStreamModel(t, stream.Body.Bytes(), "DGX-SPARK")

	suffixed := h.chat(t, "DGX-SPARK(high)", false)
	if suffixed.Code != http.StatusOK || upstream.lastReceived() != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("thinking suffix: %d upstream=%q %s", suffixed.Code, upstream.lastReceived(), suffixed.Body.String())
	}

	models = h.do(t, http.MethodGet, "/v1/models", "")
	if !strings.Contains(models.Body.String(), `"DGX-SPARK"`) || strings.Contains(models.Body.String(), "NVFP4") {
		t.Fatalf("models after swap: %s", models.Body.String())
	}
	current, errRead := os.ReadFile(h.configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(current, h.configRaw) {
		t.Fatalf("config file changed:\n%s", current)
	}
	if source := h.service.currentSourceConfig(); source.OpenAICompatibility[0].Models[0].Name != "" {
		t.Fatalf("source config gained resolved name %q", source.OpenAICompatibility[0].Models[0].Name)
	}
}

func TestFollowSlugHotReloadStopsPoller(t *testing.T) {
	upstream := newFollowUpstream(t, "model-a")
	h := newFollowHarness(t, upstream.server.URL)
	ctx := context.Background()
	h.service.slugs.Start(ctx)
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if running := h.service.slugs.Running(); running != 1 {
		t.Fatalf("running pollers = %d, want 1", running)
	}

	withoutFollow := h.service.currentSourceConfig().CloneForRuntime()
	withoutFollow.OpenAICompatibility[0].Models = []config.OpenAICompatibilityModel{{Name: "model-a", Alias: "DGX-SPARK"}}
	h.service.applyWatcherConfigUpdate(withoutFollow)
	if running := h.service.slugs.Running(); running != 0 {
		t.Fatalf("running pollers after removing follow entry = %d, want 0", running)
	}

	withFollow := withoutFollow.CloneForRuntime()
	withFollow.OpenAICompatibility[0].Models = []config.OpenAICompatibilityModel{{Alias: "DGX-SPARK", Follow: true}}
	h.service.applyWatcherConfigUpdate(withFollow)
	if running := h.service.slugs.Running(); running != 1 {
		t.Fatalf("running pollers after re-adding follow entry = %d, want 1", running)
	}

	withoutGroup := withFollow.CloneForRuntime()
	withoutGroup.OpenAICompatibility = nil
	h.service.applyWatcherConfigUpdate(withoutGroup)
	if running := h.service.slugs.Running(); running != 0 {
		t.Fatalf("running pollers after removing group = %d, want 0", running)
	}
	if statuses := h.service.slugs.Statuses(); len(statuses) != 0 {
		t.Fatalf("statuses after removing group = %+v", statuses)
	}
}

func (h *followHarness) modelsStatus(t *testing.T, key string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec.Code
}

// A slug resolution that commits between a reload's commit and that reload's
// runtime apply supersedes the reload; it must still publish the reloaded
// config to the HTTP server so revoked client keys stop working.
func TestSlugResolutionDoesNotDropPendingServerReload(t *testing.T) {
	upstream := newFollowUpstream(t, "model-a")
	h := newFollowHarness(t, upstream.server.URL)
	ctx := context.Background()
	if code := h.modelsStatus(t, "slug-client-key"); code != http.StatusOK {
		t.Fatalf("old key before reload: status %d, want 200", code)
	}

	reloaded := h.service.currentSourceConfig().CloneForRuntime()
	reloaded.APIKeys = []string{"replacement-key"}
	// The watcher's reload commits, then pauses before its runtime apply ...
	reload := h.service.commitConfigUpdate(reloaded)
	if reload.cfg == nil {
		t.Fatal("reload commit rejected")
	}
	// ... while a slug resolution commits and applies synchronously.
	h.service.slugs.Start(ctx)
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if h.service.configCommitCurrent(reload) {
		t.Fatal("slug resolution did not supersede the reload commit")
	}
	// The reload's apply now finds itself superseded and returns early.
	if h.service.applyConfigRuntime(ctx, reload, false) {
		t.Fatal("superseded reload apply reported success")
	}

	if code := h.modelsStatus(t, "replacement-key"); code != http.StatusOK {
		t.Fatalf("replacement key after reload: status %d, want 200", code)
	}
	if code := h.modelsStatus(t, "slug-client-key"); code != http.StatusUnauthorized {
		t.Fatalf("revoked key after reload: status %d, want 401", code)
	}
}

// Slug-only applies skip server updates once the loaded config has reached the
// server, and every new reload reaches it.
func TestSlugResolutionSkipsServerUpdateForAppliedSource(t *testing.T) {
	upstream := newFollowUpstream(t, "model-a")
	h := newFollowHarness(t, upstream.server.URL)
	ctx := context.Background()
	var mu sync.Mutex
	var published []*config.Config
	server := h.service.server
	h.service.updateServerClientsContextFn = func(ctx context.Context, cfg *config.Config) bool {
		mu.Lock()
		published = append(published, cfg)
		mu.Unlock()
		return server.UpdateClientsContext(ctx, cfg)
	}
	h.service.applyPprofConfigContextFn = func(context.Context, *config.Config) bool { return true }
	publishedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(published)
	}

	h.service.slugs.Start(ctx)
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if n := publishedCount(); n != 0 {
		t.Fatalf("slug resolution on the built config published %d server updates, want 0", n)
	}

	reloaded := h.service.currentSourceConfig().CloneForRuntime()
	reloaded.APIKeys = []string{"replacement-key"}
	h.service.applyWatcherConfigUpdate(reloaded)
	if n := publishedCount(); n != 1 {
		t.Fatalf("reload published %d server updates, want 1", n)
	}

	upstream.serve("model-b")
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if n := publishedCount(); n != 1 {
		t.Fatalf("slug resolution after an applied reload published %d server updates, want 1", n)
	}
	mu.Lock()
	last := published[len(published)-1]
	mu.Unlock()
	if last != reloaded {
		t.Fatal("server did not receive the reloaded source config")
	}
	if got := h.service.currentConfig().OpenAICompatibility[0].Models[0].Name; got != "model-b" {
		t.Fatalf("effective model after swap = %q, want model-b", got)
	}
}
