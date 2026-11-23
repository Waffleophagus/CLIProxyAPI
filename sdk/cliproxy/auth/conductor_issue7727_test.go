package auth

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7727Executor struct {
	provider string

	refreshStarted chan struct{}
	releaseRefresh chan struct{}
	refreshOnce    sync.Once
	inRefresh      atomic.Bool
	refreshSetsPID bool

	prepareStarted chan struct{}
	releasePrepare chan struct{}
	prepareOnce    sync.Once

	prepareCalls           atomic.Int32
	prepareOverlapDetected atomic.Bool

	mu                    sync.Mutex
	prepareObservedTokens []string
	prepareObservedIDs    []string
	prepareFn             func(context.Context, *Auth) (*Auth, error)
}

func (e *issue7727Executor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "issue7727"
}

func (e *issue7727Executor) ShouldPrepareRequestAuth(auth *Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return true
	}
	return testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *issue7727Executor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.inRefresh.Load() {
		e.prepareOverlapDetected.Store(true)
	}
	e.prepareCalls.Add(1)
	if auth != nil {
		e.mu.Lock()
		e.prepareObservedTokens = append(e.prepareObservedTokens, testStringValue(auth.Metadata["access_token"]))
		e.prepareObservedIDs = append(e.prepareObservedIDs, auth.ID)
		e.mu.Unlock()
	}
	if e.prepareFn != nil {
		return e.prepareFn(ctx, auth)
	}
	if e.prepareStarted != nil {
		e.prepareOnce.Do(func() { close(e.prepareStarted) })
	}
	if e.releasePrepare != nil {
		select {
		case <-e.releasePrepare:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project-" + testStringValue(updated.Metadata["access_token"])
	return updated, nil
}

func (e *issue7727Executor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.inRefresh.Store(true)
	defer e.inRefresh.Store(false)
	if e.refreshStarted != nil {
		e.refreshOnce.Do(func() { close(e.refreshStarted) })
	}
	if e.releaseRefresh != nil {
		select {
		case <-e.releaseRefresh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = "refreshed-token"
	if e.refreshSetsPID {
		updated.Metadata["project_id"] = "project-from-refresh"
	}
	return updated, nil
}

func (e *issue7727Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7727Executor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue7727Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7727Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func waitForRefreshLockWaiters(t *testing.T, manager *Manager, id string, want int32, abortCh <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if abortCh != nil {
			select {
			case <-abortCh:
				t.Fatalf("caller finished without blocking on refreshLocks[%q]", id)
			default:
			}
		}
		if lockValue, ok := manager.refreshLocks.Load(id); ok {
			if lock, okLock := lockValue.(*authRefreshLock); okLock && lock != nil && lock.waiters.Load() >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d waiters on refreshLocks[%q]", want, id)
		}
		runtime.Gosched()
	}
}

func TestIssue7727_PrepareRequestAuthSerializesWithRefreshLock(t *testing.T) {
	t.Run("waits for refresh and skips prepare when refresh populates project_id", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		exec := &issue7727Executor{
			provider:       "issue7727",
			refreshStarted: make(chan struct{}),
			releaseRefresh: make(chan struct{}),
			refreshSetsPID: true,
		}
		manager.RegisterExecutor(exec)

		auth := &Auth{
			ID:       "ag-serialize-skip",
			Provider: "issue7727",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "old-token",
				"refresh_token": "rt-1",
			},
		}
		if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		refreshDone := make(chan error, 1)
		go func() {
			_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
			refreshDone <- errRef
		}()
		<-exec.refreshStarted

		type prepareOutcome struct {
			auth *Auth
			err  error
		}
		prepareDone := make(chan prepareOutcome, 1)
		prepareFinished := make(chan struct{})
		go func() {
			prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
			prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
			close(prepareFinished)
		}()

		// Deterministically confirm PrepareRequestAuth is blocked on m.refreshLocks[id] before releasing Refresh.
		waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

		close(exec.releaseRefresh)
		if errRef := <-refreshDone; errRef != nil {
			t.Fatalf("refreshAuthForRequest error: %v", errRef)
		}
		outcome := <-prepareDone
		if outcome.err != nil {
			t.Fatalf("PrepareRequestAuth error: %v", outcome.err)
		}
		if exec.prepareOverlapDetected.Load() {
			t.Fatal("PrepareRequestAuth ran concurrently with Refresh")
		}
		if got := exec.prepareCalls.Load(); got != 0 {
			t.Fatalf("PrepareRequestAuth calls = %d, want 0 (Refresh already populated project_id)", got)
		}
		if got := testStringValue(outcome.auth.Metadata["access_token"]); got != "refreshed-token" {
			t.Fatalf("access_token = %q, want refreshed-token", got)
		}
		if got := testStringValue(outcome.auth.Metadata["project_id"]); got != "project-from-refresh" {
			t.Fatalf("project_id = %q, want project-from-refresh", got)
		}
	})

	t.Run("waits for refresh and sees refreshed token before preparing", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		exec := &issue7727Executor{
			provider:       "issue7727",
			refreshStarted: make(chan struct{}),
			releaseRefresh: make(chan struct{}),
			refreshSetsPID: false,
		}
		manager.RegisterExecutor(exec)

		auth := &Auth{
			ID:       "ag-serialize-token",
			Provider: "issue7727",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "old-token",
				"refresh_token": "rt-2",
			},
		}
		if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		refreshDone := make(chan error, 1)
		go func() {
			_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
			refreshDone <- errRef
		}()
		<-exec.refreshStarted

		type prepareOutcome struct {
			auth *Auth
			err  error
		}
		prepareDone := make(chan prepareOutcome, 1)
		prepareFinished := make(chan struct{})
		go func() {
			prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
			prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
			close(prepareFinished)
		}()

		// Deterministically confirm PrepareRequestAuth is blocked on m.refreshLocks[id] before releasing Refresh.
		waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

		close(exec.releaseRefresh)
		if errRef := <-refreshDone; errRef != nil {
			t.Fatalf("refreshAuthForRequest error: %v", errRef)
		}
		outcome := <-prepareDone
		if outcome.err != nil {
			t.Fatalf("PrepareRequestAuth error: %v", outcome.err)
		}
		if exec.prepareOverlapDetected.Load() {
			t.Fatal("PrepareRequestAuth overlapped with Refresh")
		}
		exec.mu.Lock()
		tokens := append([]string(nil), exec.prepareObservedTokens...)
		exec.mu.Unlock()
		if len(tokens) != 1 || tokens[0] != "refreshed-token" {
			t.Fatalf("PrepareRequestAuth observed tokens = %v, want [refreshed-token]", tokens)
		}
	})
}

func TestIssue7727_PrepareRequestAuthAbortsWhenCredentialUnregistered(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	exec := &issue7727Executor{
		provider:       "issue7727",
		refreshStarted: make(chan struct{}),
		releaseRefresh: make(chan struct{}),
	}
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "ag-unregistered-while-waiting",
		Provider: "issue7727",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "rt-unreg",
		},
	}
	if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRef
	}()
	<-exec.refreshStarted

	type prepareOutcome struct {
		auth *Auth
		err  error
	}
	prepareDone := make(chan prepareOutcome, 1)
	prepareFinished := make(chan struct{})
	go func() {
		prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
		prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
		close(prepareFinished)
	}()

	// Ensure PrepareRequestAuth is already waiting on the lock before removing the credential.
	waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

	manager.Remove(ctx, auth.ID)

	close(exec.releaseRefresh)
	<-refreshDone

	outcome := <-prepareDone
	if outcome.err == nil {
		t.Fatalf("PrepareRequestAuth succeeded for unregistered credential: auth=%#v", outcome.auth)
	}
	if got := exec.prepareCalls.Load(); got != 0 {
		t.Fatalf("preparer.PrepareRequestAuth calls = %d, want 0 for unregistered credential", got)
	}
}

func TestIssue7727_ExecutePathsDoNotPanicWhenCredentialRemovedDuringPrepare(t *testing.T) {
	const model = "issue7727-model"
	paths := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.Execute(ctx, []string{"issue7727"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "ExecuteCount",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.ExecuteCount(ctx, []string{"issue7727"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "ExecuteStream",
			run: func(m *Manager, ctx context.Context) error {
				res, err := m.ExecuteStream(ctx, []string{"issue7727"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if err != nil {
					return err
				}
				for range res.Chunks {
				}
				return nil
			},
		},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			exec := &issue7727Executor{
				provider: "issue7727",
				prepareFn: func(_ context.Context, a *Auth) (*Auth, error) {
					// Simulate credential deletion mid-preparation so UpdatePreparedAuth returns (nil, nil).
					manager.Remove(ctx, a.ID)
					updated := a.Clone()
					updated.Metadata["project_id"] = "prepared-project"
					return updated, nil
				},
			}
			manager.RegisterExecutor(exec)

			auth := &Auth{
				ID:       "ag-removed-mid-prepare-" + tc.name,
				Provider: "issue7727",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "tok"},
			}
			if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
				t.Fatalf("Register error: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "issue7727", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

			errRun := tc.run(manager, ctx)
			if errRun == nil {
				t.Fatalf("%s expected error when credential removed during preparation, got nil", tc.name)
			}
		})
	}
}

func TestIssue7727_PrepareRequestAuthHonorsContextCancellationWhileWaitingForLock(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	exec := &issue7727Executor{
		provider:       "issue7727",
		prepareStarted: make(chan struct{}),
		releasePrepare: make(chan struct{}),
	}
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "ag-cancel-lock-wait",
		Provider: "issue7727",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "tok"},
	}
	if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
		firstDone <- errPrep
	}()
	<-exec.prepareStarted

	waitCtx, cancelWait := context.WithCancel(ctx)
	secondDone := make(chan error, 1)
	secondFinished := make(chan struct{})
	go func() {
		_, errPrep := manager.PrepareRequestAuth(waitCtx, exec, auth.Clone())
		secondDone <- errPrep
		close(secondFinished)
	}()
	waitForRefreshLockWaiters(t, manager, auth.ID, 1, secondFinished)
	cancelWait()

	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case errSecond := <-secondDone:
		if !errors.Is(errSecond, context.Canceled) {
			close(exec.releasePrepare)
			<-firstDone
			t.Fatalf("second PrepareRequestAuth error = %v, want context.Canceled", errSecond)
		}
	case <-timer.C:
		close(exec.releasePrepare)
		<-firstDone
		<-secondDone
		t.Fatal("canceled PrepareRequestAuth remained blocked waiting on refresh lock")
	}

	close(exec.releasePrepare)
	if errFirst := <-firstDone; errFirst != nil {
		t.Fatalf("first PrepareRequestAuth error: %v", errFirst)
	}
}
