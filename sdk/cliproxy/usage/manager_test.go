package usage

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"
	"weak"
)

func TestStreamFromContextDefaultsMissingToFalse(t *testing.T) {
	if StreamFromContext(context.Background()) {
		t.Fatalf("StreamFromContext(background) = true, want false")
	}
}

func TestStreamFromContextHonorsExplicitTrue(t *testing.T) {
	ctx := WithStream(context.Background(), true)
	if !StreamFromContext(ctx) {
		t.Fatalf("StreamFromContext(true) = false, want true")
	}
}

func TestRecordStreamField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		Stream:   true,
	}
	if !record.Stream {
		t.Fatalf("Record.Stream = false, want true")
	}
}

func TestRecordBaseURLField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		BaseURL:  "https://custom-gateway.example.com/v1",
	}
	if record.BaseURL != "https://custom-gateway.example.com/v1" {
		t.Fatalf("Record.BaseURL = %q, want %q", record.BaseURL, "https://custom-gateway.example.com/v1")
	}
}

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

func TestAccessProviderAndIsNativeKeyFromContext(t *testing.T) {
	bg := context.Background()
	if provider := AccessProviderFromContext(bg); provider != "" {
		t.Fatalf("AccessProviderFromContext(bg) = %q, want empty", provider)
	}
	if _, ok := IsNativeKeyFromContext(bg); ok {
		t.Fatalf("IsNativeKeyFromContext(bg) reported ok, want false")
	}

	ctx := WithAccessProvider(bg, "config-inline")
	if provider := AccessProviderFromContext(ctx); provider != "config-inline" {
		t.Fatalf("AccessProviderFromContext(ctx) = %q, want %q", provider, "config-inline")
	}

	ctxNative := WithIsNativeKey(ctx, true)
	if val, ok := IsNativeKeyFromContext(ctxNative); !ok || !val {
		t.Fatalf("IsNativeKeyFromContext(ctxNative) = (%v, %v), want (true, true)", val, ok)
	}

	ctxNonNative := WithIsNativeKey(ctx, false)
	if val, ok := IsNativeKeyFromContext(ctxNonNative); !ok || val {
		t.Fatalf("IsNativeKeyFromContext(ctxNonNative) = (%v, %v), want (false, true)", val, ok)
	}
}

func TestIsNativeAccessProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"config-inline", true},
		{"CONFIG-INLINE", true},
		{"config-api-key", true},
		{"CONFIG-API-KEY", true},
		{"  config-inline  ", true},
		{"custom-plugin", false},
		{"realtime-client-secret", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsNativeAccessProvider(tt.provider); got != tt.want {
			t.Errorf("IsNativeAccessProvider(%q) = %v, want %v", tt.provider, got, tt.want)
		}
	}
}

func TestRecordNativeKeyFields(t *testing.T) {
	record := Record{
		Provider:       "openai",
		Model:          "gpt-5.4",
		APIKey:         "sk-test-123",
		IsNativeKey:    true,
		AccessProvider: "config-inline",
	}
	if !record.IsNativeKey {
		t.Fatalf("Record.IsNativeKey = false, want true")
	}
	if record.AccessProvider != "config-inline" {
		t.Fatalf("Record.AccessProvider = %q, want %q", record.AccessProvider, "config-inline")
	}
}

type captureUsagePlugin struct {
	records chan Record
}

func (p *captureUsagePlugin) HandleUsage(_ context.Context, record Record) {
	p.records <- record
}

func TestManagerPublishBackfillsContextAuthFields(t *testing.T) {
	mgr := NewManager(10)
	t.Cleanup(mgr.Stop)
	plugin := &captureUsagePlugin{records: make(chan Record, 1)}
	mgr.Register(plugin)

	ctx := WithAPIKey(context.Background(), "sk-ctx-key")
	ctx = WithAccessProvider(ctx, "config-inline")

	mgr.Publish(ctx, Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	})

	select {
	case got := <-plugin.records:
		if got.APIKey != "sk-ctx-key" {
			t.Fatalf("got.APIKey = %q, want sk-ctx-key", got.APIKey)
		}
		if got.AccessProvider != "config-inline" {
			t.Fatalf("got.AccessProvider = %q, want config-inline", got.AccessProvider)
		}
		if !got.IsNativeKey {
			t.Fatalf("got.IsNativeKey = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestManagerStopDrainsAndOnlyExplicitStartRestarts(t *testing.T) {
	mgr := NewManager(10)
	plugin := &captureUsagePlugin{records: make(chan Record, 4)}
	mgr.Register(plugin)
	mgr.Publish(context.Background(), Record{Model: "first"})
	mgr.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errWait := mgr.Wait(ctx); errWait != nil {
		t.Fatalf("Wait: %v", errWait)
	}
	// Records queued before Stop are delivered before Wait returns.
	select {
	case got := <-plugin.records:
		if got.Model != "first" {
			t.Fatalf("got.Model = %q", got.Model)
		}
	default:
		t.Fatal("queued record was not delivered before Wait returned")
	}

	// A late publication (e.g. from a request goroutine that outlived the HTTP
	// server) is dropped and does not restart the dispatcher.
	mgr.Publish(context.Background(), Record{Model: "late"})
	mgr.mu.Lock()
	running, queued := mgr.running, len(mgr.queue)
	mgr.mu.Unlock()
	if running || queued != 0 || mgr.Dropped() != 1 {
		t.Fatalf("after late publish: running=%v queued=%d dropped=%d, want stopped and 1 dropped", running, queued, mgr.Dropped())
	}
	if errWait := mgr.Wait(ctx); errWait != nil {
		t.Fatalf("Wait after late publish: %v", errWait)
	}

	// An explicit Start (e.g. a second service run) reopens the manager.
	mgr.Start(context.Background())
	mgr.Publish(context.Background(), Record{Model: "second"})
	select {
	case got := <-plugin.records:
		if got.Model != "second" {
			t.Fatalf("got.Model = %q", got.Model)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restarted dispatcher did not deliver")
	}
	mgr.Stop()
	if errWait := mgr.Wait(ctx); errWait != nil {
		t.Fatalf("Wait after restart: %v", errWait)
	}
}

func TestManagerStopBeforeStartDropsPublications(t *testing.T) {
	mgr := NewManager(10)
	mgr.Stop()
	mgr.Publish(context.Background(), Record{Model: "m"})
	mgr.mu.Lock()
	running := mgr.running
	mgr.mu.Unlock()
	if running || mgr.Dropped() != 1 {
		t.Fatalf("running=%v dropped=%d, want a stopped manager to stay stopped", running, mgr.Dropped())
	}
}

// ginPathStub fails the test if the pooled gin context is read at publish time.
type ginPathStub struct {
	t    *testing.T
	path string
}

func (g ginPathStub) FullPath() string {
	g.t.Errorf("Publish read gin.Context.FullPath; the gin context may already be recycled")
	return g.path
}

func (g ginPathStub) Get(string) (any, bool) {
	g.t.Errorf("Publish read gin.Context.Get; the gin context may already be recycled")
	return "sk-from-recycled-gin-context", true
}

func TestManagerPublishUsesCapturedEndpointNotGinContext(t *testing.T) {
	mgr := NewManager(10)
	t.Cleanup(mgr.Stop)
	plugin := &captureUsagePlugin{records: make(chan Record, 1)}
	mgr.Register(plugin)
	// The handler captured the route on the request goroutine; by publish time
	// the gin context belongs to a different request.
	ctx := WithEndpoint(context.Background(), "/v1/messages")
	ctx = context.WithValue(ctx, "gin", ginPathStub{t: t, path: "/v1/other-request"})
	mgr.Publish(ctx, Record{Model: "m"})
	select {
	case got := <-plugin.records:
		if got.Endpoint != "/v1/messages" {
			t.Fatalf("got.Endpoint = %q, want /v1/messages", got.Endpoint)
		}
		if got.APIKey != "" {
			t.Fatalf("got.APIKey = %q, read from the gin context", got.APIKey)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

type notifyUsagePlugin struct {
	delivered chan struct{}
}

func (p *notifyUsagePlugin) HandleUsage(_ context.Context, _ Record) {
	p.delivered <- struct{}{}
}

type retainedRequestPayload struct {
	data [64 << 10]byte
}

type retainedRequestContextKey struct{}

//go:noinline
func publishConsumableRecord(mgr *Manager) (weak.Pointer[retainedRequestPayload], weak.Pointer[string]) {
	ctxPayload := &retainedRequestPayload{}
	headerSlice := []string{"req-header-value"}

	ctx := context.WithValue(context.Background(), retainedRequestContextKey{}, ctxPayload)
	mgr.Publish(ctx, Record{
		Provider:        "openai",
		Model:           "gpt-5.4",
		ResponseHeaders: http.Header{"X-Request-Id": headerSlice},
	})

	return weak.Make(ctxPayload), weak.Make(&headerSlice[0])
}

func TestManagerReleasesConsumedQueueItemReferencesWhenIdle(t *testing.T) {
	mgr := NewManager(10)
	t.Cleanup(mgr.Stop)

	plugin := &notifyUsagePlugin{delivered: make(chan struct{}, 1)}
	mgr.Register(plugin)

	ctxWeak, headerWeak := publishConsumableRecord(mgr)

	select {
	case <-plugin.delivered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage record dispatch")
	}

	for range 10 {
		runtime.Gosched()
		runtime.GC()
		if ctxWeak.Value() == nil && headerWeak.Value() == nil {
			break
		}
	}

	if ctxWeak.Value() != nil {
		t.Fatalf("consumed request context payload is still retained while manager is idle")
	}
	if headerWeak.Value() != nil {
		t.Fatalf("consumed record ResponseHeaders backing value is still retained while manager is idle")
	}
}
