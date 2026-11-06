package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type credentialTerminalResponseError struct{ error }

func (e credentialTerminalResponseError) Unwrap() error          { return e.error }
func (credentialTerminalResponseError) IsCredentialScoped() bool { return true }

func TestManagerTerminalResponsePolicy(t *testing.T) {
	{
		for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
			for _, policy := range []string{"default", "wrapped", "stop", "stop-and-cooldown", "continue", "continue-and-cooldown", "credential", "ordinary"} {
				t.Run(fmt.Sprintf("%s/%s", mode, policy), func(t *testing.T) {
					terminal := &cliproxyexecutor.RequestTerminatedError{
						HTTPStatus: http.StatusBadGateway,
						Header:     http.Header{"X-Terminal-Response": []string{"preserved"}},
						Body:       []byte(`{"detail":"request outcome is unknown"}`),
					}
					var failure error = terminal
					var rules []internalconfig.RequestScopedErrorRule
					switch policy {
					case "wrapped":
						failure = fmt.Errorf("executor response: %w", terminal)
					case "credential":
						terminal.HTTPStatus = http.StatusTooManyRequests
						failure = credentialTerminalResponseError{error: terminal}
					case "ordinary":
						failure = &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary upstream failure"}
					case "default":
					default:
						rules = []internalconfig.RequestScopedErrorRule{{Status: terminal.HTTPStatus, Match: []string{"outcome is unknown"}, Action: policy}}
					}
					metadata := map[string]any{"request_scoped_errors": rules}
					executor := &terminalResponseTestExecutor{failure: failure, streamBootstrap: mode == "bootstrap"}
					manager := NewManager(nil, &FillFirstSelector{}, nil)
					manager.SetRetryConfig(1, 0, 2)
					manager.RegisterExecutor(executor)
					const model = "terminal-response-model"
					for _, id := range []string{"home-retry-a", "home-retry-b"} {
						registry.GetGlobalRegistry().RegisterClient(id, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: executor.Identifier(), Status: StatusActive, Metadata: metadata}); errRegister != nil {
							t.Fatal(errRegister)
						}
					}
					errExecute := invokeTerminalResponse(manager, executor.Identifier(), model, mode)
					stop := policy == "default" || policy == "wrapped" || policy == "stop" || policy == "stop-and-cooldown"
					wantCalls := []string{"home-retry-a"}
					if stop {
						requireTerminalResponse(t, errExecute, terminal)
					} else {
						wantCalls = append(wantCalls, "home-retry-b")
						if errExecute != nil {
							t.Fatalf("safe credential failover failed: %v", errExecute)
						}
					}
					if calls := executor.Calls(); !reflect.DeepEqual(calls, wantCalls) {
						t.Fatalf("credential executions = %v, want %v", calls, wantCalls)
					}
					if policy == "default" || policy == "wrapped" || policy == "stop" {
						auth, _ := manager.GetByID("home-retry-a")
						if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
							t.Fatalf("terminal response cooled credential: %+v", auth)
						}
					}
				})
			}
		}
	}
}

func TestManagerTerminalUnauthorizedDoesNotRefresh(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusUnauthorized, Body: []byte(`{"detail":"terminal rejection"}`)}
			executor := &claudeCancellationTestExecutor{
				executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, terminal
				},
				countFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, terminal
				},
				streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
					if mode == "bootstrap" {
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Err: terminal}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					}
					return nil, terminal
				},
			}
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)
			requireTerminalResponse(t, invokeTerminalResponse(manager, executor.Identifier(), model, mode), terminal)
			if calls := executor.executeCalls.Load() + executor.countCalls.Load() + executor.streamCalls.Load(); calls != 1 || executor.refreshCalls.Load() != 0 {
				t.Fatalf("terminal 401 was retried or refreshed: executions=%d refreshes=%d", calls, executor.refreshCalls.Load())
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}

func invokeTerminalResponse(manager *Manager, provider, model, mode string) error {
	req := cliproxyexecutor.Request{Model: model}
	switch mode {
	case "execute":
		_, errExecute := manager.Execute(context.Background(), []string{provider}, req, cliproxyexecutor.Options{})
		return errExecute
	case "count":
		_, errCount := manager.ExecuteCount(context.Background(), []string{provider}, req, cliproxyexecutor.Options{})
		return errCount
	default:
		stream, errStream := manager.ExecuteStream(context.Background(), []string{provider}, req, cliproxyexecutor.Options{Stream: true})
		if errStream != nil {
			return errStream
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	}
}

func requireTerminalResponse(t *testing.T, err error, terminal *cliproxyexecutor.RequestTerminatedError) {
	t.Helper()
	var response *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &response) || response != terminal {
		t.Fatalf("response = %v, want original terminal response", err)
	}
	var downstream interface {
		StatusCode() int
		ResponseHeaders() http.Header
		ResponseBody() []byte
	}
	if !errors.As(err, &downstream) || downstream.StatusCode() != terminal.HTTPStatus ||
		!reflect.DeepEqual(downstream.ResponseHeaders(), terminal.Header) || string(downstream.ResponseBody()) != string(terminal.Body) {
		t.Fatalf("downstream status, headers or body changed: %v", err)
	}
}

// terminalResponseTestExecutor fails every call on home-retry-a and succeeds on
// any other credential, recording the order in which credentials were tried.
type terminalResponseTestExecutor struct {
	mu              sync.Mutex
	calls           []string
	failure         error
	streamBootstrap bool
}

func (*terminalResponseTestExecutor) Identifier() string { return "terminal-response-test" }

func (e *terminalResponseTestExecutor) record(authID string) bool {
	e.mu.Lock()
	e.calls = append(e.calls, authID)
	e.mu.Unlock()
	return authID == "home-retry-a"
}

func (e *terminalResponseTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.record(auth.ID) {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return cliproxyexecutor.Response{}, e.failure
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *terminalResponseTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.record(auth.ID) {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		if e.streamBootstrap {
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Err: e.failure}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		}
		return nil, e.failure
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *terminalResponseTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*terminalResponseTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (*terminalResponseTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *terminalResponseTestExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}
