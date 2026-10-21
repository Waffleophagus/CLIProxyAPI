package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestore"
)

func usageTestRouter(t *testing.T, runtime *usagestore.Runtime) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.SetUsageRuntime(runtime)
	r := gin.New()
	r.GET("/summary", h.GetUsageSummary)
	r.GET("/timeseries", h.GetUsageTimeseries)
	r.GET("/requests", h.GetUsageRequests)
	return r
}

func getJSON(t *testing.T, r http.Handler, target string, wantStatus int) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != wantStatus {
		t.Fatalf("GET %s = %d, want %d: %s", target, rec.Code, wantStatus, rec.Body.String())
	}
	var out map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &out); errDecode != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), errDecode)
	}
	return out
}

func TestUsageEndpointsDisabled(t *testing.T) {
	runtime := usagestore.NewRuntime(nil)
	runtime.Apply(usagestore.Settings{Enabled: false}, "")
	r := usageTestRouter(t, runtime)
	for _, target := range []string{"/summary", "/timeseries", "/requests"} {
		body := getJSON(t, r, target, http.StatusConflict)
		if body["error"] != "usage tracking is disabled" {
			t.Fatalf("%s body = %v", target, body)
		}
	}
	// A nil runtime (SDK embedders without the store) reports the same.
	getJSON(t, usageTestRouter(t, nil), "/summary", http.StatusConflict)
}

func TestUsageEndpointsQueries(t *testing.T) {
	runtime := usagestore.NewRuntime(nil)
	runtime.Apply(usagestore.Settings{Enabled: true, Path: filepath.Join(t.TempDir(), "usage.db"), RetentionDays: -1}, "")
	t.Cleanup(func() { _ = runtime.Close() })
	store, errStore := runtime.Store()
	if errStore != nil {
		t.Fatal(errStore)
	}
	now := time.Now()
	keyID, keyMasked := usagestore.ClientKeyIdentity("sk-dashboard-client-key-0001")
	for i := 0; i < 5; i++ {
		store.Enqueue(usagestore.Row{
			TS:              now.Add(-time.Duration(i+1) * time.Minute).UnixMilli(),
			Provider:        "openai-compatibility",
			RequestedModel:  map[bool]string{true: "DGX-SPARK", false: "other"}[i < 3],
			UpstreamModel:   "GLM",
			AuthID:          "auth-1",
			AuthLabel:       "spark",
			ClientKeyID:     keyID,
			ClientKeyMasked: keyMasked,
			Success:         i != 4,
			Status:          map[bool]int{true: 200, false: 502}[i != 4],
			LatencyMs:       100,
			InputTokens:     10,
			OutputTokens:    5,
			TotalTokens:     15,
		})
	}
	// A row outside the default 24h window.
	store.Enqueue(usagestore.Row{TS: now.Add(-48 * time.Hour).UnixMilli(), RequestedModel: "old", Success: true, TotalTokens: 1000})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errFlush := store.Flush(ctx); errFlush != nil {
		t.Fatal(errFlush)
	}
	r := usageTestRouter(t, runtime)

	summary := getJSON(t, r, "/summary?group_by=model&tz=UTC", http.StatusOK)
	totals := summary["totals"].(map[string]any)
	if totals["requests"].(float64) != 5 || totals["failures"].(float64) != 1 || totals["total_tokens"].(float64) != 75 {
		t.Fatalf("totals = %v", totals)
	}
	groups := summary["groups"].([]any)
	first := groups[0].(map[string]any)
	if len(groups) != 2 || first["key"] != "DGX-SPARK" || first["requests"].(float64) != 3 {
		t.Fatalf("groups = %v", groups)
	}
	if summary["timezone"] != "UTC" || summary["group_by"] != "model" {
		t.Fatalf("summary meta = %v", summary)
	}

	// Explicit windows accept unix milliseconds and RFC3339.
	from := strconv.FormatInt(now.Add(-72*time.Hour).UnixMilli(), 10)
	wide := getJSON(t, r, "/summary?from="+from+"&to="+now.UTC().Format(time.RFC3339), http.StatusOK)
	if wide["totals"].(map[string]any)["requests"].(float64) != 6 {
		t.Fatalf("wide totals = %v", wide["totals"])
	}
	keyed := getJSON(t, r, "/summary?group_by=api_key", http.StatusOK)
	keyGroup := keyed["groups"].([]any)[0].(map[string]any)
	if keyGroup["key"] != keyID || keyGroup["label"] != keyMasked {
		t.Fatalf("api_key group = %v", keyGroup)
	}

	series := getJSON(t, r, "/timeseries?bucket=hour&group_by=model&top=1", http.StatusOK)
	if buckets := series["buckets"].([]any); len(buckets) < 24 || len(buckets) > 25 {
		t.Fatalf("bucket count = %d", len(buckets))
	}
	lines := series["series"].([]any)
	if len(lines) != 2 || lines[0].(map[string]any)["key"] != "DGX-SPARK" || lines[1].(map[string]any)["key"] != usagestore.OtherSeriesKey {
		t.Fatalf("series = %v", lines)
	}

	page := getJSON(t, r, "/requests?limit=2&model=DGX-SPARK", http.StatusOK)
	rows := page["requests"].([]any)
	if len(rows) != 2 || page["next_before"] == nil {
		t.Fatalf("page = %v", page)
	}
	next := strconv.FormatInt(int64(page["next_before"].(float64)), 10)
	page = getJSON(t, r, "/requests?limit=2&model=DGX-SPARK&before="+next, http.StatusOK)
	if rows = page["requests"].([]any); len(rows) != 1 || page["next_before"] != nil {
		t.Fatalf("last page = %v", page)
	}
	failed := getJSON(t, r, "/requests?success=false", http.StatusOK)
	if rows = failed["requests"].([]any); len(rows) != 1 || rows[0].(map[string]any)["status"].(float64) != 502 {
		t.Fatalf("failed rows = %v", rows)
	}
	all := getJSON(t, r, "/requests", http.StatusOK)
	if rows = all["requests"].([]any); len(rows) != 6 {
		t.Fatalf("open-window rows = %d, want 6", len(rows))
	}

	for _, target := range []string{
		"/summary?group_by=bogus",
		"/summary?from=yesterday",
		"/summary?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"/summary?tz=Not/AZone",
		"/timeseries?bucket=week",
		"/timeseries?group_by=day",
		"/timeseries?top=0",
		"/timeseries?bucket=hour&from=0",
		"/requests?limit=9999",
		"/requests?before=-1",
		"/requests?success=maybe",
	} {
		getJSON(t, r, target, http.StatusBadRequest)
	}
}
