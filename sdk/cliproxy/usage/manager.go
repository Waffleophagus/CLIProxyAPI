package usage

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
)

// DefaultServiceTier is retained for direct SDK and non-OpenAI usage callers.
const DefaultServiceTier = "default"

// AutoServiceTier is the OpenAI request semantics when service_tier is omitted.
// OpenAI HTTP handlers set it explicitly, without changing other providers'
// historical direct-SDK default.
const AutoServiceTier = "auto"

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	// RequestID uniquely identifies this specific model execution instance (UUID v4).
	RequestID string
	// TraceID identifies the parent inbound HTTP request when available (8-character hex).
	TraceID  string
	Provider string
	// BaseURL stores the configured upstream base URL when available.
	BaseURL string
	// ExecutorType stores the concrete executor type that handled the request.
	ExecutorType string
	Model        string
	Alias        string
	APIKey       string
	// IsNativeKey reports whether the client API key was authenticated by CPA's native config provider.
	IsNativeKey bool
	// AccessProvider identifies the client request authentication provider (e.g. "config-inline" or plugin ID).
	AccessProvider  string
	SessionID       string
	ParentSessionID string
	AuthID          string
	AuthIndex       string
	// AccessTokenSHA256 identifies the OAuth token version without exposing the token.
	AccessTokenSHA256 string
	AuthType          string
	Source            string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier.
	ServiceTier string
	// RequestServiceTier is a deprecated input-only alias retained for existing
	// plugin callers. It is normalized into ServiceTier and never emitted.
	RequestServiceTier string
	// ResponseServiceTier stores the final tier reported by the upstream response.
	ResponseServiceTier string
	// ResponseModel stores the model name reported by the upstream response, empty when unknown.
	ResponseModel string
	// Generate reports whether the client requested actual generation.
	// nil or true means generation is enabled; only an explicit false disables generation.
	// Use GenerateFlag to set the value and GenerateEnabled to read it with the default.
	Generate *bool
	// Stream reports whether the request was executed in streaming mode.
	Stream bool
	// Endpoint stores the inbound route (e.g. "/v1/chat/completions") when known.
	Endpoint    string
	RequestedAt time.Time
	Latency     time.Duration
	TTFT        time.Duration
	Failed      bool
	Fail        Failure
	Detail      Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
}

// Failure holds HTTP failure metadata for an upstream request attempt.
type Failure struct {
	StatusCode int
	Body       string
}

// Detail holds the token usage breakdown.
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	TokenBreakdown      TokenBreakdown
	ResponseServiceTier string
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}
type generateContextKey struct{}
type streamContextKey struct{}
type executionRequestIDContextKey struct{}
type executionTraceIDContextKey struct{}
type apiKeyContextKey struct{}
type accessProviderContextKey struct{}
type isNativeKeyContextKey struct{}
type endpointContextKey struct{}

// WithExecutionRequestID attaches a specific execution instance request ID to the context.
func WithExecutionRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionRequestIDContextKey{}, strings.TrimSpace(requestID))
}

// ExecutionRequestIDFromContext retrieves the execution instance request ID from the context.
func ExecutionRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionRequestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTraceID attaches the parent inbound HTTP request ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionTraceIDContextKey{}, strings.TrimSpace(traceID))
}

// TraceIDFromContext retrieves the parent inbound HTTP request ID from the context.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionTraceIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestedModelAlias stores the client-requested model name for usage sinks.
func WithRequestedModelAlias(ctx context.Context, alias string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedModelAliasContextKey{}, alias)
}

// RequestedModelAliasFromContext returns the client-requested model name stored in ctx.
func RequestedModelAliasFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(requestedModelAliasContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithReasoningEffort stores the client-requested reasoning effort for usage sinks.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

// ReasoningEffortFromContext returns the client-requested reasoning effort stored in ctx.
func ReasoningEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(reasoningEffortContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithServiceTier stores the client-requested service tier for usage sinks.
func WithServiceTier(ctx context.Context, tier string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = DefaultServiceTier
	}
	return context.WithValue(ctx, serviceTierContextKey{}, tier)
}

// ServiceTierFromContext returns the client-requested service tier stored in ctx.
func ServiceTierFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultServiceTier
	}
	raw := ctx.Value(serviceTierContextKey{})
	switch value := raw.(type) {
	case string:
		tier := strings.TrimSpace(value)
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	case []byte:
		tier := strings.TrimSpace(string(value))
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	default:
		return DefaultServiceTier
	}
}

// WithGenerate stores whether the client requested actual generation for usage sinks.
// Missing context values default to true; only an explicit false disables generation.
func WithGenerate(ctx context.Context, generate bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, generateContextKey{}, generate)
}

// GenerateFromContext returns whether the client requested actual generation.
// Missing values default to true.
func GenerateFromContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	raw := ctx.Value(generateContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return true
	}
}

// WithStream stores whether the request was executed in streaming mode for usage sinks.
func WithStream(ctx context.Context, stream bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, streamContextKey{}, stream)
}

// StreamFromContext returns whether the request was executed in streaming mode.
// Missing values default to false.
func StreamFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(streamContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return false
	}
}

// WithAccessProvider stores the client authentication provider identifier for usage sinks.
func WithAccessProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ctx
	}
	return context.WithValue(ctx, accessProviderContextKey{}, provider)
}

// WithAPIKey stores the client API key for usage sinks.
func WithAPIKey(ctx context.Context, apiKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ctx
	}
	return context.WithValue(ctx, apiKeyContextKey{}, apiKey)
}

// stringContextValue returns a trimmed string (or []byte) context value.
func stringContextValue(ctx context.Context, key any) string {
	if ctx == nil {
		return ""
	}
	switch value := ctx.Value(key).(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	return ""
}

// APIKeyFromContext returns the client API key stored in ctx or gin.Context.
// The gin fallback must only be used on the request goroutine.
func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if key := stringContextValue(ctx, apiKeyContextKey{}); key != "" {
		return key
	}
	if ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) }); ok && ginCtx != nil {
		if v, exists := ginCtx.Get("userApiKey"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case fmt.Stringer:
				return strings.TrimSpace(value.String())
			default:
				return strings.TrimSpace(fmt.Sprintf("%v", value))
			}
		}
	}
	return ""
}

// WithEndpoint stores the inbound route template (e.g. "/v1/chat/completions")
// for usage sinks. Call it on the request goroutine, before any asynchronous
// work, with the value read from the gin context.
func WithEndpoint(ctx context.Context, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ctx
	}
	return context.WithValue(ctx, endpointContextKey{}, endpoint)
}

// EndpointFromContext returns the route template stored by WithEndpoint. It
// deliberately never reads the gin.Context carried by ctx: usage records are
// often published from streaming goroutines after the handler returned and the
// pooled gin.Context was reset for another request.
func EndpointFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	endpoint, _ := ctx.Value(endpointContextKey{}).(string)
	return endpoint
}

// AccessProviderFromContext returns the client authentication provider stored
// in ctx or gin.Context. The gin fallback must only be used on the request
// goroutine.
func AccessProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if provider := stringContextValue(ctx, accessProviderContextKey{}); provider != "" {
		return provider
	}
	if ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) }); ok && ginCtx != nil {
		if v, exists := ginCtx.Get("accessProvider"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case []byte:
				return strings.TrimSpace(string(value))
			}
		}
	}
	return ""
}

// WithIsNativeKey stores whether the client API key is native to CPA for usage sinks.
func WithIsNativeKey(ctx context.Context, isNative bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, isNativeKeyContextKey{}, isNative)
}

// IsNativeKeyFromContext returns whether the client API key is native to CPA if explicitly set in ctx.
func IsNativeKeyFromContext(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	raw := ctx.Value(isNativeKeyContextKey{})
	if value, ok := raw.(bool); ok {
		return value, true
	}
	return false, false
}

// IsNativeAccessProvider reports whether provider represents CPA's built-in inline key provider.
func IsNativeAccessProvider(provider string) bool {
	p := strings.TrimSpace(provider)
	return strings.EqualFold(p, "config-inline") || strings.EqualFold(p, "config-api-key")
}

// GenerateFlag returns a pointer suitable for Record.Generate.
func GenerateFlag(generate bool) *bool {
	return &generate
}

// GenerateEnabled reports whether generation is enabled for the record field.
// A nil value defaults to true so legacy callers that omit Generate keep the historical behavior.
func GenerateEnabled(generate *bool) bool {
	if generate == nil {
		return true
	}
	return *generate
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager maintains a queue of usage records and delivers them to registered plugins.
//
// Lifecycle: the first Publish lazily starts the dispatcher if Start was never
// called. Stop closes the manager: it stops accepting records (later
// publications are dropped and counted), and the dispatcher delivers what is
// already queued and exits. A stopped manager stays stopped until an explicit
// Start, e.g. by a new service in the same process; publications never restart it.
type Manager struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []queueItem
	closed  bool
	running bool
	done    chan struct{}
	dropped atomic.Int64

	pluginsMu sync.RWMutex
	plugins   []Plugin
	named     map[string]int
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	m := &Manager{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start (re)opens the manager and launches the background dispatcher. If a
// stopped dispatcher is still draining, it keeps running as the active one.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = false
	m.startLocked()
}

// startLocked launches a dispatcher unless one is running. m.mu must be held.
func (m *Manager) startLocked() {
	if m.running {
		return
	}
	m.running = true
	done := make(chan struct{})
	m.done = done
	go m.run(done)
}

// Stop closes the manager: new publications are dropped, and the dispatcher
// delivers the queued records and exits. It does not wait; use Wait to block
// until the queue is drained.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cond.Broadcast()
}

// Dropped returns how many records were published while the manager was stopped.
func (m *Manager) Dropped() int64 {
	if m == nil {
		return 0
	}
	return m.dropped.Load()
}

// Wait blocks until the current dispatcher has exited after Stop, or ctx ends.
func (m *Manager) Wait(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Register appends a plugin to the delivery list.
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// RegisterNamed registers or replaces a plugin by name.
func (m *Manager) RegisterNamed(name string, plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	m.pluginsMu.Lock()
	if m.named == nil {
		m.named = make(map[string]int)
	}
	if index, exists := m.named[name]; exists && index >= 0 && index < len(m.plugins) {
		m.plugins[index] = plugin
		m.pluginsMu.Unlock()
		return
	}
	m.named[name] = len(m.plugins)
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	if strings.TrimSpace(record.RequestID) == "" {
		if reqID := ExecutionRequestIDFromContext(ctx); reqID != "" {
			record.RequestID = reqID
		} else {
			record.RequestID = uuid.NewString()
		}
	}
	if strings.TrimSpace(record.TraceID) == "" {
		if trID := TraceIDFromContext(ctx); trID != "" {
			record.TraceID = trID
		} else if trID := internallogging.GetRequestID(ctx); trID != "" {
			record.TraceID = trID
		}
	}
	// Publish may run on a streaming goroutine after the handler returned, so
	// only immutable context values are read here, never the pooled gin.Context.
	if strings.TrimSpace(record.APIKey) == "" {
		if key := stringContextValue(ctx, apiKeyContextKey{}); key != "" {
			record.APIKey = key
		}
	}
	if strings.TrimSpace(record.AccessProvider) == "" {
		if provider := stringContextValue(ctx, accessProviderContextKey{}); provider != "" {
			record.AccessProvider = provider
		}
	}
	if !record.IsNativeKey {
		if nativeExplicit, ok := IsNativeKeyFromContext(ctx); ok {
			record.IsNativeKey = nativeExplicit
		} else if record.AccessProvider != "" {
			record.IsNativeKey = IsNativeAccessProvider(record.AccessProvider)
		}
	}
	if strings.TrimSpace(record.Endpoint) == "" {
		record.Endpoint = EndpointFromContext(ctx)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		dropped := m.dropped.Add(1)
		log.Debugf("usage: manager stopped; dropping usage record (provider=%s model=%s dropped=%d)", record.Provider, record.Model, dropped)
		return
	}
	// Lazily start the dispatcher if Start was never called; a stopped manager
	// is closed above and is never restarted implicitly.
	m.startLocked()
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) run(done chan struct{}) {
	defer close(done)
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.running = false
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue[0] = queueItem{}
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

var defaultManager = NewManager(512)

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// RegisterNamedPlugin registers or replaces a named plugin on the default manager.
func RegisterNamedPlugin(name string, plugin Plugin) { DefaultManager().RegisterNamed(name, plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }

// WaitDefault blocks until the default manager's dispatcher has drained after StopDefault.
func WaitDefault(ctx context.Context) error { return DefaultManager().Wait(ctx) }
