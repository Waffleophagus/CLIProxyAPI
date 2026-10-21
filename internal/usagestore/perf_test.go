package usagestore

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
)

// TestLargeWindowQueryTimings measures the management queries on a synthetic
// database spanning a year of personal usage: a dozen model routes, each
// with a fixed provider, account and upstream model. It is skipped unless
// USAGESTORE_PERF_ROWS is set, e.g.
//
//	USAGESTORE_PERF_ROWS=1000000 go test -run TestLargeWindowQueryTimings -v ./internal/usagestore/
//
// USAGESTORE_PERF_DB reuses (or creates) the database at that path.
func TestLargeWindowQueryTimings(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("USAGESTORE_PERF_ROWS"))
	if n <= 0 {
		t.Skip("set USAGESTORE_PERF_ROWS to run")
	}
	path := os.Getenv("USAGESTORE_PERF_DB")
	if path == "" {
		path = filepath.Join(t.TempDir(), "perf.db")
	}
	end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	start := end.Add(-365 * 24 * time.Hour)
	store, errOpen := Open(Options{Path: path, Tick: make(chan time.Time), Now: func() time.Time { return end }})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = store.Close() }()
	if countRows(t, store) < n {
		type route struct{ provider, auth, authType, model, upstream string }
		var routes []route
		for i := 0; i < 12; i++ {
			routes = append(routes, route{
				provider: []string{"claude", "codex", "openai-compatible-spark", "xai"}[i%4],
				auth:     fmt.Sprintf("auth-%d", i%5),
				authType: []string{"oauth", "oauth", "", "api_key"}[i%4],
				model:    fmt.Sprintf("model-%d", i),
				upstream: fmt.Sprintf("upstream-%d", i),
			})
		}
		rng := rand.New(rand.NewSource(1))
		span := end.Sub(start).Milliseconds()
		batch := make([]Row, 0, 10000)
		// Requests arrive in agent sessions: a run of 10-60 requests on one
		// route and client key whose context grows as the session goes on.
		// USAGESTORE_PERF_RANDOM=1 picks every dimension per request instead
		// (the worst case for rollups).
		randomDims := os.Getenv("USAGESTORE_PERF_RANDOM") != ""
		var r route
		var key string
		var left int
		var context int64
		for i := 0; i < n; i++ {
			if left == 0 || randomDims {
				r = routes[min(rng.Intn(len(routes)), rng.Intn(len(routes)))] // skewed toward the first routes
				key = fmt.Sprintf("k%d", min(rng.Intn(3), rng.Intn(3)))
				left = 10 + rng.Intn(50)
				context = int64(10_000 + rng.Intn(20_000))
			}
			left--
			context += int64(rng.Intn(8000))
			input := context
			if randomDims {
				input = int64(rng.Intn(250_000))
			}
			batch = append(batch, Row{
				TS:              start.UnixMilli() + int64(i)*span/int64(n),
				RequestID:       fmt.Sprintf("req-%d", i),
				Provider:        r.provider,
				AuthID:          r.auth,
				AuthType:        r.authType,
				RequestedModel:  r.model,
				UpstreamModel:   r.upstream,
				ClientKeyID:     key,
				Success:         rng.Intn(20) != 0,
				Status:          200,
				LatencyMs:       int64(rng.Intn(20000)),
				TTFTMs:          int64(rng.Intn(2000)),
				InputTokens:     input,
				CacheReadTokens: int64(rng.Intn(100_000)),
				OutputTokens:    int64(rng.Intn(8000)),
				TotalTokens:     int64(rng.Intn(250_000)),
			})
			if len(batch) == cap(batch) || i == n-1 {
				if errInsert := store.insert(batch); errInsert != nil {
					t.Fatal(errInsert)
				}
				batch = batch[:0]
			}
		}
	}
	var rollups int
	if errScan := store.DB().QueryRow("SELECT COUNT(*) FROM rollups").Scan(&rollups); errScan != nil {
		t.Fatal(errScan)
	}
	t.Logf("%d requests, %d rollup rows", countRows(t, store), rollups)
	ctx := context.Background()
	catalog, errCatalog := pricing.ParseCatalog([]byte(pricingTestCatalog))
	if errCatalog != nil {
		t.Fatal(errCatalog)
	}
	pricer := pricing.NewPricer(catalog, config.UsagePricingConfig{})
	measure := func(name string, fn func() error) {
		began := time.Now()
		if errRun := fn(); errRun != nil {
			t.Fatalf("%s: %v", name, errRun)
		}
		t.Logf("%-36s %v", name, time.Since(began).Round(time.Millisecond))
	}
	for _, w := range []struct {
		name string
		days int
	}{{"24h", 1}, {"30d", 30}, {"90d", 90}, {"365d", 365}} {
		window := Filter{From: end.Add(-time.Duration(w.days) * 24 * time.Hour).Add(7 * time.Minute), To: end.Add(-3 * time.Minute), Pricer: pricer}
		bucket := "day"
		if w.days == 1 {
			bucket = "hour"
		}
		measure(w.name+" priced summary by model", func() error { _, err := store.SummaryQuery(ctx, window, "model", time.UTC); return err })
		measure(w.name+" priced timeseries by model", func() error {
			_, err := store.TimeseriesQuery(ctx, window, bucket, "model", 7, time.UTC)
			return err
		})
		measure(w.name+" latency percentiles only", func() error { _, err := store.latencyPercentiles(ctx, window, 0.5, 0.95); return err })
	}
	window := Filter{From: start, To: end}
	measure("requests latest 50", func() error { _, _, err := store.RequestsQuery(ctx, window, 0, 50); return err })
	filtered := window
	filtered.RequestedModel = "model-3"
	measure("requests model latest 50", func() error { _, _, err := store.RequestsQuery(ctx, filtered, 0, 50); return err })
}
