package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7725UnauthorizedExecutor struct {
	mu             sync.Mutex
	executeIndices []any
	countIndices   []any
	executeCalls   int
	countCalls     int
	refreshCalls   int
	refreshedToken string
	invalidToken   string
	onUnauthorized func(*Auth)
}

func (*issue7725UnauthorizedExecutor) Identifier() string { return "codex" }

func (e *issue7725UnauthorizedExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls++
	e.executeIndices = append(e.executeIndices, opts.Metadata[cliproxyexecutor.SelectedAuthIndexMetadataKey])
	token := authAccessToken(auth)
	invalid := token == e.invalidToken
	onUnauth := e.onUnauthorized
	e.mu.Unlock()
	if invalid {
		if onUnauth != nil {
			onUnauth(auth)
		}
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed"}`)}, nil
}

func (*issue7725UnauthorizedExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *issue7725UnauthorizedExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.countCalls++
	e.countIndices = append(e.countIndices, opts.Metadata[cliproxyexecutor.SelectedAuthIndexMetadataKey])
	token := authAccessToken(auth)
	invalid := token == e.invalidToken
	onUnauth := e.onUnauthorized
	e.mu.Unlock()
	if invalid {
		if onUnauth != nil {
			onUnauth(auth)
		}
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":1}`)}, nil
}

func (e *issue7725UnauthorizedExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	refreshedToken := e.refreshedToken
	e.mu.Unlock()
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = refreshedToken
	return updated, nil
}

func (*issue7725UnauthorizedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_ExecuteAndCount_UnauthorizedRefreshPublishesSelectedAuthMetadata_Issue7725(t *testing.T) {
	newSetup := func(t *testing.T, withConcurrentIndexUpdate bool) (*Manager, *issue7725UnauthorizedExecutor, string) {
		t.Helper()
		model := "gpt-5.5-issue7725"
		auth := &Auth{
			ID:       "auth-issue7725",
			Provider: "codex",
			Index:    "idx-before-refresh",
			Metadata: map[string]any{
				"access_token":  "stale-token",
				"refresh_token": "valid-refresh-token",
			},
		}
		var m *Manager
		executor := &issue7725UnauthorizedExecutor{
			invalidToken:   "stale-token",
			refreshedToken: "fresh-token",
		}
		if withConcurrentIndexUpdate {
			executor.onUnauthorized = func(a *Auth) {
				updated := a.Clone()
				updated.Index = "idx-after-refresh"
				updated.indexAssigned = true
				updated.Metadata["access_token"] = "fresh-token"
				if _, errUpd := m.Update(context.Background(), updated); errUpd != nil {
					t.Errorf("concurrent Update error: %v", errUpd)
				}
			}
		}
		m = NewManager(nil, nil, nil)
		m.RegisterExecutor(executor)
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}
		return m, executor, model
	}

	t.Run("Execute publishes refreshed auth metadata on 401 retry", func(t *testing.T) {
		m, executor, model := newSetup(t, true)
		var callbackAuthIDs []string
		var callbackAuthIndices []string
		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(id string) {
					callbackAuthIDs = append(callbackAuthIDs, id)
				},
				cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(idx string) {
					callbackAuthIndices = append(callbackAuthIndices, idx)
				},
			},
		}

		_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
		if errExec != nil {
			t.Fatalf("Execute error = %v", errExec)
		}
		if len(executor.executeIndices) != 2 {
			t.Fatalf("execute call count = %d, want 2", len(executor.executeIndices))
		}
		if executor.executeIndices[0] != "idx-before-refresh" {
			t.Fatalf("first attempt SelectedAuthIndex = %v, want idx-before-refresh", executor.executeIndices[0])
		}
		if executor.executeIndices[1] != "idx-after-refresh" {
			t.Fatalf("post-401 retry SelectedAuthIndex = %v, want idx-after-refresh", executor.executeIndices[1])
		}
		if len(callbackAuthIDs) != 2 || len(callbackAuthIndices) != 2 {
			t.Fatalf("callbacks = (ids=%v, indices=%v), want 2 invocations each", callbackAuthIDs, callbackAuthIndices)
		}
		if callbackAuthIndices[1] != "idx-after-refresh" {
			t.Fatalf("second callback index = %q, want idx-after-refresh", callbackAuthIndices[1])
		}
	})

	t.Run("ExecuteCount publishes refreshed auth metadata on 401 retry", func(t *testing.T) {
		m, executor, model := newSetup(t, true)
		var callbackAuthIDs []string
		var callbackAuthIndices []string
		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(id string) {
					callbackAuthIDs = append(callbackAuthIDs, id)
				},
				cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(idx string) {
					callbackAuthIndices = append(callbackAuthIndices, idx)
				},
			},
		}

		_, errCount := m.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
		if errCount != nil {
			t.Fatalf("ExecuteCount error = %v", errCount)
		}
		if len(executor.countIndices) != 2 {
			t.Fatalf("count call count = %d, want 2", len(executor.countIndices))
		}
		if executor.countIndices[0] != "idx-before-refresh" {
			t.Fatalf("first attempt SelectedAuthIndex = %v, want idx-before-refresh", executor.countIndices[0])
		}
		if executor.countIndices[1] != "idx-after-refresh" {
			t.Fatalf("post-401 retry SelectedAuthIndex = %v, want idx-after-refresh", executor.countIndices[1])
		}
		if len(callbackAuthIDs) != 2 || len(callbackAuthIndices) != 2 {
			t.Fatalf("callbacks = (ids=%v, indices=%v), want 2 invocations each", callbackAuthIDs, callbackAuthIndices)
		}
		if callbackAuthIndices[1] != "idx-after-refresh" {
			t.Fatalf("second callback index = %q, want idx-after-refresh", callbackAuthIndices[1])
		}
	})

	t.Run("Execute and ExecuteCount invoke callbacks after Refresh path", func(t *testing.T) {
		m, executor, model := newSetup(t, false)
		var callbackCount int
		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) {
					callbackCount++
				},
			},
		}
		if _, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts); errExec != nil {
			t.Fatalf("Execute error = %v", errExec)
		}
		if executor.refreshCalls != 1 {
			t.Fatalf("Execute refreshCalls = %d, want 1", executor.refreshCalls)
		}
		if callbackCount != 2 {
			t.Fatalf("Execute callbackCount = %d, want 2", callbackCount)
		}

		mCount, execCount, modelCount := newSetup(t, false)
		var countCallback int
		optsCount := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) {
					countCallback++
				},
			},
		}
		if _, errCount := mCount.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: modelCount}, optsCount); errCount != nil {
			t.Fatalf("ExecuteCount error = %v", errCount)
		}
		if execCount.refreshCalls != 1 {
			t.Fatalf("ExecuteCount refreshCalls = %d, want 1", execCount.refreshCalls)
		}
		if countCallback != 2 {
			t.Fatalf("ExecuteCount callbackCount = %d, want 2", countCallback)
		}
	})
}
