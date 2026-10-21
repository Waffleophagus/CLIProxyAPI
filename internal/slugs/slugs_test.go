package slugs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// manualTicker never fires on its own; tests drive polls through Refresh.
type manualTicker struct{ c chan time.Time }

func (t manualTicker) C() <-chan time.Time { return t.c }
func (t manualTicker) Stop()               {}

func newManualTicker(time.Duration) Ticker { return manualTicker{c: make(chan time.Time)} }

type fakeEndpoint struct {
	mu    sync.Mutex
	ids   []string
	err   error
	calls int
}

func (f *fakeEndpoint) set(ids []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids, f.err = ids, err
}

func (f *fakeEndpoint) fetch(_ context.Context, _ Target) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]string(nil), f.ids...), f.err
}

func followConfig(models ...config.OpenAICompatibilityModel) *config.Config {
	return &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "dgx-spark",
		BaseURL: "http://spark.invalid/v1",
		Models:  models,
	}}}
}

func startManager(t *testing.T, endpoint *fakeEndpoint, cfg *config.Config) *Manager {
	t.Helper()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	m := NewManager(Options{Fetch: endpoint.fetch, NewTicker: newManualTicker, Now: func() time.Time { return now }})
	m.Reconfigure(cfg)
	m.Start(context.Background())
	t.Cleanup(m.Stop)
	if err := m.Refresh(context.Background(), ""); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return m
}

func resolvedName(t *testing.T, m *Manager, cfg *config.Config, index int) config.OpenAICompatibilityModel {
	t.Helper()
	return m.Effective(cfg).OpenAICompatibility[0].Models[index]
}

func TestSelect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ids   []string
		match string
		want  string
		ok    bool
	}{
		{"single", []string{"GLM-5.3-Flash-EXL3"}, "", "GLM-5.3-Flash-EXL3", true},
		{"multiple with match", []string{"embed-small", "GLM-5.3-Flash-NVFP4", "GLM-other"}, "glm-*", "GLM-5.3-Flash-NVFP4", true},
		{"multiple match infix", []string{"a/GLM-x-EXL3", "b/GLM-y-NVFP4"}, "*NVFP4", "b/GLM-y-NVFP4", true},
		{"multiple without match", []string{"first", "second"}, "", "first", true},
		{"match without hit", []string{"first", "second"}, "GLM-*", "", false},
		{"empty list", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Select(tc.ids, tc.match)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Select(%v, %q) = %q, %t; want %q, %t", tc.ids, tc.match, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestManagerResolvesAndFollowsSwap(t *testing.T) {
	endpoint := &fakeEndpoint{ids: []string{"GLM-5.3-Flash-EXL3"}}
	cfg := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	m := startManager(t, endpoint, cfg)

	model := resolvedName(t, m, cfg, 0)
	if model.Name != "GLM-5.3-Flash-EXL3" || model.FollowPending || !model.ForceMapping {
		t.Fatalf("effective model = %+v, want resolved EXL3 with force-mapping", model)
	}
	if cfg.OpenAICompatibility[0].Models[0].Name != "" || cfg.OpenAICompatibility[0].Models[0].ForceMapping {
		t.Fatal("Effective must not modify the source config")
	}

	endpoint.set([]string{"GLM-5.3-Flash-NVFP4"}, nil)
	if err := m.Refresh(context.Background(), "DGX-Spark"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := resolvedName(t, m, cfg, 0).Name; got != "GLM-5.3-Flash-NVFP4" {
		t.Fatalf("resolved after swap = %q", got)
	}
}

func TestManagerKeepsLastValueOnErrorOrEmptyList(t *testing.T) {
	endpoint := &fakeEndpoint{ids: []string{"model-a"}}
	cfg := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	m := startManager(t, endpoint, cfg)

	endpoint.set(nil, errors.New("connection refused"))
	if err := m.Refresh(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := resolvedName(t, m, cfg, 0).Name; got != "model-a" {
		t.Fatalf("resolved after error = %q, want model-a", got)
	}
	status := m.Statuses()[0]
	if status.LastError != "connection refused" || status.ResolvedModel != "model-a" || status.LastSuccessAt == nil {
		t.Fatalf("status after error = %+v", status)
	}

	endpoint.set([]string{}, nil)
	if err := m.Refresh(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := resolvedName(t, m, cfg, 0).Name; got != "model-a" {
		t.Fatalf("resolved after empty list = %q, want model-a", got)
	}
	if status = m.Statuses()[0]; status.LastError == "" || len(status.ReportedModels) != 1 {
		t.Fatalf("status after empty list = %+v", status)
	}
}

func TestManagerMatchAndPinnedEntries(t *testing.T) {
	endpoint := &fakeEndpoint{ids: []string{"embed-small", "GLM-5.3-Flash-EXL3"}}
	cfg := followConfig(
		config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true, Match: "GLM-*"},
		config.OpenAICompatibilityModel{Alias: "DGX-EMBED", Follow: true},
		config.OpenAICompatibilityModel{Alias: "DGX-PINNED", Follow: true, Name: "GLM-pinned"},
	)
	m := startManager(t, endpoint, cfg)
	effective := m.Effective(cfg).OpenAICompatibility[0].Models
	if effective[0].Name != "GLM-5.3-Flash-EXL3" || effective[1].Name != "embed-small" {
		t.Fatalf("effective names = %q, %q", effective[0].Name, effective[1].Name)
	}
	if effective[2].Name != "GLM-pinned" || !effective[2].ForceMapping || effective[2].FollowPending {
		t.Fatalf("pinned entry = %+v", effective[2])
	}
	statuses := m.Statuses()
	if len(statuses) != 3 || statuses[0].Mode != ModeFollow || statuses[2].Mode != ModePinned || statuses[2].ResolvedModel != "GLM-pinned" {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestEffectiveMarksUnresolvedFollowPending(t *testing.T) {
	cfg := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	var m *Manager
	model := m.Effective(cfg).OpenAICompatibility[0].Models[0]
	if model.Name != "DGX-SPARK" || !model.FollowPending || !model.ForceMapping {
		t.Fatalf("pending model = %+v", model)
	}
	plain := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "x", BaseURL: "http://x", Models: []config.OpenAICompatibilityModel{{Name: "a", Alias: "b"}}}}}
	if NewManager(Options{}).Effective(plain) != plain {
		t.Fatal("configs without follow entries must be returned unchanged")
	}
}

func TestReconfigureStopsRemovedPollersAndKeepsState(t *testing.T) {
	endpoint := &fakeEndpoint{ids: []string{"model-a"}}
	cfg := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	m := startManager(t, endpoint, cfg)
	if m.Running() != 1 {
		t.Fatalf("running = %d, want 1", m.Running())
	}

	changed := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	changed.OpenAICompatibility[0].FollowInterval = "1m"
	endpoint.set(nil, errors.New("down"))
	m.Reconfigure(changed)
	if got := resolvedName(t, m, changed, 0).Name; got != "model-a" {
		t.Fatalf("resolved after reconfigure = %q, want carried-over model-a", got)
	}

	pinned := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true, Name: "fixed"})
	m.Reconfigure(pinned)
	if m.Running() != 0 {
		t.Fatalf("running after pin = %d, want 0", m.Running())
	}

	m.Reconfigure(cfg)
	if m.Running() != 1 {
		t.Fatalf("running after re-enable = %d, want 1", m.Running())
	}
	m.Reconfigure(&config.Config{})
	if m.Running() != 0 || len(m.Statuses()) != 0 {
		t.Fatalf("running after removal = %d, statuses = %d", m.Running(), len(m.Statuses()))
	}
	if err := m.Refresh(context.Background(), "dgx-spark"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("refresh removed group = %v, want ErrGroupNotFound", err)
	}
}

func TestOnChangeRunsBeforeRefreshReturns(t *testing.T) {
	endpoint := &fakeEndpoint{ids: []string{"model-a"}}
	cfg := followConfig(config.OpenAICompatibilityModel{Alias: "DGX-SPARK", Follow: true})
	var mu sync.Mutex
	var applied []string
	m := NewManager(Options{Fetch: endpoint.fetch, NewTicker: newManualTicker})
	m.SetOnChange(func(context.Context) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, m.Effective(cfg).OpenAICompatibility[0].Models[0].Name)
	})
	m.Reconfigure(cfg)
	m.Start(context.Background())
	defer m.Stop()
	if err := m.Refresh(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	endpoint.set([]string{"model-b"}, nil)
	if err := m.Refresh(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 2 || applied[0] != "model-a" || applied[1] != "model-b" {
		t.Fatalf("applied = %v, want [model-a model-b]", applied)
	}
}

func TestFetchModelsUsesKeyAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-test" || r.Header.Get("X-Team") != "lab" || r.Header.Get("X-Session") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"GLM-5.3-Flash-EXL3","object":"model","owned_by":"tensorfold"},{"id":"second"}]}`))
	}))
	defer server.Close()
	ids, err := FetchModels(context.Background(), Target{
		BaseURL: server.URL + "/v1/",
		APIKey:  "sk-test",
		Headers: map[string]string{"X-Team": "lab", "X-Session": "$X-Session"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "GLM-5.3-Flash-EXL3" || ids[1] != "second" {
		t.Fatalf("ids = %v", ids)
	}
	if _, err = FetchModels(context.Background(), Target{BaseURL: server.URL + "/other"}); err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}
