package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

func TestClaudeAutoRefreshDeferralDoesNotClearPendingBackoffToZero(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	deferredErr := fmt.Errorf("Claude renewal deferred until interactive macOS wake: %w", baseauth.ErrRefreshDeferred)
	executor := &unauthorizedRefreshExecutor{id: "claude", refreshErr: deferredErr}
	manager.RegisterExecutor(executor)

	now := time.Now()
	auth := &Auth{
		ID:       "claude-wake-deferred-job",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "valid-access",
			"refresh_token": "claude-sleep-refresh",
			"expired":       now.Add(4 * time.Hour).Format(time.RFC3339),
		},
	}
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
	job := manager.markRefreshPending(loop, registered.ID, registered.RegistrationEpoch, now)
	if job == nil {
		t.Fatal("expected pending refresh job")
	}
	if !manager.beginRefreshJob(ctx, job) {
		t.Fatal("expected refresh job to start")
	}
	_, errRefresh := manager.refreshAuthForRequestAtEpoch(ctx, job.id, "", job.registrationEpoch)
	if errRefresh == nil {
		t.Fatal("expected deferred refresh error")
	}
	manager.finishRefreshJob(job, time.Time{}, false)

	updated, ok := manager.GetByID(registered.ID)
	if !ok || updated == nil {
		t.Fatal("credential missing after deferred refresh")
	}
	if updated.Unavailable || updated.Status != StatusActive {
		t.Fatalf("wake deferral marked credential unhealthy: %+v", updated)
	}
	if updated.LastError != nil {
		t.Fatalf("wake deferral recorded error state: last=%v", updated.LastError)
	}
	if updated.NextRefreshAfter.IsZero() || !updated.NextRefreshAfter.After(now) {
		t.Fatalf("NextRefreshAfter = %v, want a positive backoff after %v to avoid a tight loop", updated.NextRefreshAfter, now)
	}
}
