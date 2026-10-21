package handlers

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type modelExecutionCaptureExecutor struct {
	provider string

	mu          sync.Mutex
	lastRequest coreexecutor.Request
	lastOptions coreexecutor.Options
	execute     func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	stream      func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (e *modelExecutionCaptureExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "codex"
}

func (e *modelExecutionCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return coreexecutor.Response{Payload: []byte("model-execution-ok")}, nil
}

func (e *modelExecutionCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(req, opts)
	if e.stream != nil {
		return e.stream(ctx, auth, req, opts)
	}
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *modelExecutionCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *modelExecutionCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte("0")}, nil
}

func (e *modelExecutionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *modelExecutionCaptureExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRequest = coreexecutor.Request{
		Model:    req.Model,
		Payload:  cloneBytes(req.Payload),
		Format:   req.Format,
		Metadata: req.Metadata,
	}
	e.lastOptions = coreexecutor.Options{
		Stream:          opts.Stream,
		Alt:             opts.Alt,
		Headers:         cloneHeader(opts.Headers),
		Query:           cloneURLValues(opts.Query),
		OriginalRequest: cloneBytes(opts.OriginalRequest),
		SourceFormat:    opts.SourceFormat,
		ResponseFormat:  opts.ResponseFormat,
		Metadata:        opts.Metadata,
	}
}

func (e *modelExecutionCaptureExecutor) captured() (coreexecutor.Request, coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastRequest, e.lastOptions
}

func newModelExecutionHandler(t *testing.T, model string, executor *modelExecutionCaptureExecutor, cfg *sdkconfig.SDKConfig) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "model-execution-" + model,
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": model + "@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	return NewBaseAPIHandlers(cfg, manager)
}

func TestExecuteModelStreamContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage)
	chunks := wrapModelExecutionChunks(ctx, dataChan, errChan, nil)

	cancel()

	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	select {
	case chunk, ok := <-chunks:
		if ok {
			t.Fatalf("stream chunks yielded after cancel: %+v", chunk)
		}
	case <-timeout.C:
		t.Fatal("stream chunks did not close after context cancellation")
	}
}

func TestExecuteProtocolWithAuthManagerUsesForcedProvider(t *testing.T) {
	model := "interactions-agent-target"
	requestBody := []byte(`{"agent":"agents/test-agent","input":"hi"}`)
	executor := &modelExecutionCaptureExecutor{
		provider: "gemini",
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{Payload: []byte(`{"id":"interaction_1"}`)}, nil
		},
	}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{})

	resp, errMsg := handler.ExecuteProtocolWithAuthManager(context.Background(), ProtocolExecutionRequest{
		EntryProtocol:  "interactions",
		ExitProtocol:   "interactions",
		ForcedProvider: "gemini",
		Model:          model,
		Body:           requestBody,
	})
	if errMsg != nil {
		t.Fatalf("ExecuteProtocolWithAuthManager() error = %+v", errMsg)
	}
	if string(resp.Body) != `{"id":"interaction_1"}` {
		t.Fatalf("body = %q, want native interactions response", resp.Body)
	}

	gotReq, gotOpts := executor.captured()
	if gotReq.Model != model {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, model)
	}
	if gotOpts.SourceFormat != sdktranslator.FormatInteractions {
		t.Fatalf("SourceFormat = %q, want %q", gotOpts.SourceFormat, sdktranslator.FormatInteractions)
	}
	if gotOpts.ResponseFormat != sdktranslator.FormatInteractions {
		t.Fatalf("ResponseFormat = %q, want %q", gotOpts.ResponseFormat, sdktranslator.FormatInteractions)
	}
	if gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey] != model {
		t.Fatalf("requested model metadata = %#v, want %q", gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey], model)
	}
}

func TestProvidersForExecutionForcedGeminiUsesGeminiProvider(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	providers, model, errMsg := handler.providersForExecution("agents/test-agent", "agents/test-agent", false, modelExecutionOptions{ForcedProvider: "gemini"})
	if errMsg != nil {
		t.Fatalf("providersForExecution() error = %+v", errMsg)
	}
	if len(providers) != 1 || providers[0] != "gemini" {
		t.Fatalf("providers = %#v, want [gemini]", providers)
	}
	if model != "agents/test-agent" {
		t.Fatalf("model = %q, want agents/test-agent", model)
	}
}
