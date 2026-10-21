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
)

// TestLargeWindowQueryTimings measures the management queries on a synthetic
// database. It is skipped unless USAGESTORE_PERF_ROWS is set, e.g.
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
	start := end.Add(-30 * 24 * time.Hour)
	store, errOpen := Open(Options{Path: path, Tick: make(chan time.Time), Now: func() time.Time { return end }})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = store.Close() }()
	if countRows(t, store) < n {
		rng := rand.New(rand.NewSource(1))
		span := end.Sub(start).Milliseconds()
		batch := make([]Row, 0, 10000)
		for i := 0; i < n; i++ {
			batch = append(batch, Row{
				TS:             start.UnixMilli() + int64(i)*span/int64(n),
				RequestID:      fmt.Sprintf("req-%d", i),
				Provider:       fmt.Sprintf("p%d", rng.Intn(4)),
				AuthID:         fmt.Sprintf("auth-%d", rng.Intn(20)),
				RequestedModel: fmt.Sprintf("model-%d", rng.Intn(10)),
				UpstreamModel:  fmt.Sprintf("up-%d", rng.Intn(10)),
				ClientKeyID:    fmt.Sprintf("k%d", rng.Intn(5)),
				Success:        rng.Intn(20) != 0,
				Status:         200,
				LatencyMs:      int64(rng.Intn(5000)),
				TTFTMs:         int64(rng.Intn(500)),
				InputTokens:    int64(rng.Intn(1000)),
				OutputTokens:   int64(rng.Intn(1000)),
				TotalTokens:    int64(rng.Intn(2000)),
			})
			if len(batch) == cap(batch) || i == n-1 {
				if errInsert := store.insert(batch); errInsert != nil {
					t.Fatal(errInsert)
				}
				batch = batch[:0]
			}
		}
	}
	ctx := context.Background()
	window := Filter{From: start, To: end}
	measure := func(name string, fn func() error) {
		began := time.Now()
		if errRun := fn(); errRun != nil {
			t.Fatalf("%s: %v", name, errRun)
		}
		t.Logf("%-28s %v", name, time.Since(began).Round(time.Millisecond))
	}
	measure("summary (full window)", func() error { _, err := store.SummaryQuery(ctx, window, "", time.UTC); return err })
	measure("summary by model", func() error { _, err := store.SummaryQuery(ctx, window, "model", time.UTC); return err })
	measure("timeseries hour by model", func() error {
		_, err := store.TimeseriesQuery(ctx, window, "hour", "model", 5, time.UTC)
		return err
	})
	measure("requests latest 50", func() error { _, _, err := store.RequestsQuery(ctx, window, 0, 50); return err })
	filtered := window
	filtered.RequestedModel = "model-3"
	measure("requests model latest 50", func() error { _, _, err := store.RequestsQuery(ctx, filtered, 0, 50); return err })
	measure("requests page 2 (before)", func() error {
		_, _, err := store.RequestsQuery(ctx, window, int64(n/2), 50)
		return err
	})
}
