package cliproxy

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestore"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// TestShutdownLatePublicationDoesNotRestartDispatcher publishes a usage record
// after Shutdown, as a request goroutine that outlived the HTTP server would.
// The stopped dispatcher must stay stopped so waiting on it returns at once.
func TestShutdownLatePublicationDoesNotRestartDispatcher(t *testing.T) {
	usage.StartDefault(context.Background())
	service := &Service{usageRuntime: usagestore.NewRuntime(nil)}
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	dropped := usage.DefaultManager().Dropped()
	usage.PublishRecord(context.Background(), usage.Record{Provider: "late", Model: "m"})
	if got := usage.DefaultManager().Dropped(); got != dropped+1 {
		t.Fatalf("late publication not dropped: dropped %d -> %d", dropped, got)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if errWait := usage.WaitDefault(waitCtx); errWait != nil {
		t.Fatalf("WaitDefault after late publication: %v (dispatcher was restarted)", errWait)
	}
}

// TestUsageStoreRecordsSlugRequests sends real requests through the HTTP
// handler, auth manager and OpenAI-compatible executor to an httptest upstream
// and checks that each lands as a usage row with the slug as requested model
// and the resolved upstream model.
func TestUsageStoreRecordsSlugRequests(t *testing.T) {
	upstream := newFollowUpstream(t, "GLM-5.3-Flash-EXL3")
	h := newFollowHarness(t, upstream.server.URL)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	h.service.usageRuntime.Apply(usagestore.Settings{Enabled: true, Path: dbPath, RetentionDays: 90}, "")
	t.Cleanup(func() { _ = h.service.usageRuntime.Close() })
	store, errStore := h.service.usageRuntime.Store()
	if errStore != nil {
		t.Fatal(errStore)
	}

	ctx := context.Background()
	// Service.Run starts the usage dispatcher explicitly; an earlier test's
	// Shutdown in this package leaves the shared default manager stopped.
	usage.StartDefault(ctx)
	h.service.slugs.Start(ctx)
	if errRefresh := h.service.slugs.Refresh(ctx, ""); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if resp := h.chat(t, "DGX-SPARK", false); resp.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", resp.Code, resp.Body.String())
	}
	if resp := h.chat(t, "DGX-SPARK", true); resp.Code != http.StatusOK {
		t.Fatalf("stream chat: %d %s", resp.Code, resp.Body.String())
	}

	// Records travel through the async usage dispatcher before the store queue.
	var rows []usagestore.RequestRow
	open := usagestore.Filter{From: time.UnixMilli(0), To: time.Now().Add(time.Hour)}
	deadline := time.Now().Add(10 * time.Second)
	for len(rows) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("usage rows = %d, want 2", len(rows))
		}
		flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if errFlush := store.Flush(flushCtx); errFlush != nil {
			cancel()
			t.Fatal(errFlush)
		}
		cancel()
		var errRows error
		if rows, _, errRows = store.RequestsQuery(ctx, open, 0, 10); errRows != nil {
			t.Fatal(errRows)
		}
		if len(rows) < 2 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	stream, nonStream := rows[0], rows[1]
	for _, row := range []usagestore.RequestRow{nonStream, stream} {
		if row.RequestedModel != "DGX-SPARK" || row.UpstreamModel != "GLM-5.3-Flash-EXL3" {
			t.Fatalf("models = %q -> %q", row.RequestedModel, row.UpstreamModel)
		}
		if !row.Success || row.Status != 200 || row.Provider == "" || row.AuthID == "" {
			t.Fatalf("row outcome = %+v", row)
		}
		if row.Endpoint != "/v1/chat/completions" {
			t.Fatalf("endpoint = %q", row.Endpoint)
		}
		wantKeyID, wantMasked := usagestore.ClientKeyIdentity("slug-client-key")
		if row.ClientKeyID != wantKeyID || row.ClientKeyMasked != wantMasked {
			t.Fatalf("client key = %q %q", row.ClientKeyID, row.ClientKeyMasked)
		}
	}
	if nonStream.Stream || !stream.Stream {
		t.Fatalf("stream flags = %v / %v", nonStream.Stream, stream.Stream)
	}
	if nonStream.InputTokens != 1 || nonStream.OutputTokens != 1 || nonStream.TotalTokens != 2 {
		t.Fatalf("tokens = in %d out %d total %d", nonStream.InputTokens, nonStream.OutputTokens, nonStream.TotalTokens)
	}

	summary, errSummary := store.SummaryQuery(ctx, open, "model", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	if len(summary.Groups) != 1 || summary.Groups[0].Key != "DGX-SPARK" || summary.Groups[0].Requests != 2 {
		t.Fatalf("summary groups = %+v", summary.Groups)
	}
}
