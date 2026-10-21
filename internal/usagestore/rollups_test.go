package usagestore

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
)

func randomRows(base time.Time, n int, seed int64) []Row {
	rng := rand.New(rand.NewSource(seed))
	models := []string{"gpt-6-sol", "glm-5.3-flash", "mystery"}
	out := make([]Row, n)
	for i := range out {
		m := rng.Intn(len(models))
		success := rng.Intn(6) != 0
		out[i] = Row{
			TS:             base.Add(-time.Duration(rng.Int63n(int64(72 * time.Hour)))).UnixMilli(),
			RequestID:      fmt.Sprintf("r%d", i),
			Provider:       []string{"codex", "openai-compatible-spark", "openai-compatible-cloud"}[m],
			AuthID:         fmt.Sprintf("auth-%d", m),
			AuthLabel:      fmt.Sprintf("label-%d", m),
			AuthType:       []string{"oauth", "", "api_key"}[m],
			BaseURL:        []string{"", "http://spark.local/v1", "https://api.example.com/v1"}[m],
			RequestedModel: models[m],
			UpstreamModel:  models[m],
			ClientKeyID:    fmt.Sprintf("k%d", rng.Intn(2)),
			Success:        success,
			Status:         200,
			LatencyMs:      int64(rng.Intn(4000)),
			TTFTMs:         int64(rng.Intn(3)) * 100,
			InputTokens:    int64(rng.Intn(400_000)),
			OutputTokens:   int64(rng.Intn(5000)),
			TotalTokens:    1,
		}
		if rng.Intn(4) == 0 {
			out[i].ServiceTier = "priority"
		}
		out[i].CacheReadTokens = out[i].InputTokens / 2
	}
	return out
}

func testPricer(t *testing.T) *pricing.Pricer {
	t.Helper()
	catalog, errParse := pricing.ParseCatalog([]byte(pricingTestCatalog))
	if errParse != nil {
		t.Fatal(errParse)
	}
	return pricing.NewPricer(catalog, config.UsagePricingConfig{})
}

// TestRollupsMatchRawRows checks that rollup-backed queries (with raw rows
// filling unaligned window edges) equal a scan of the raw rows alone.
func TestRollupsMatchRawRows(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 7, 31, 0, time.UTC)
	store, _ := openTestStore(t, Options{RetentionDays: -1, BatchSize: 37})
	for _, row := range randomRows(base, 2000, 1) {
		store.Enqueue(row)
	}
	flush(t, store)
	ctx := context.Background()
	pricer := testPricer(t)
	success := true
	filters := []Filter{
		{From: base.Add(-50*time.Hour - 7*time.Minute), To: base, Pricer: pricer},
		{From: base.Add(-24 * time.Hour).Truncate(time.Hour), To: base.Truncate(time.Hour), Pricer: pricer},
		{From: base.Add(-10 * time.Minute), To: base, Pricer: pricer},
		{From: base.Add(-30 * time.Hour), To: base, RequestedModel: "gpt-6-sol", Success: &success, Pricer: pricer},
		{From: base.Add(-30 * time.Hour), To: base, RequestedModel: " gpt-6-sol ", Pricer: pricer},
		{From: base.Add(-30 * time.Hour), To: base},
	}
	for i, f := range filters {
		for _, groupBy := range []string{"", "model", "credential", "hour"} {
			var dim *Dimension
			if d, ok := Dimensions[groupBy]; ok {
				dim = &d
			}
			got, errGot := store.aggregate(ctx, f, dim, TimeBuckets[groupBy], false)
			want, errWant := rawOnly(ctx, store, f, dim, TimeBuckets[groupBy])
			if errGot != nil || errWant != nil {
				t.Fatalf("filter %d %q: %v / %v", i, groupBy, errGot, errWant)
			}
			gotTotals, wantTotals := mergeParts(got), mergeParts(want)
			if len(gotTotals) != len(wantTotals) {
				t.Fatalf("filter %d %q: %d groups, want %d", i, groupBy, len(gotTotals), len(wantTotals))
			}
			for key, w := range wantTotals {
				g := gotTotals[key]
				if g.Requests != w.Requests || g.Successes != w.Successes || g.InputTokens != w.InputTokens ||
					g.OutputTokens != w.OutputTokens || g.CacheReadTokens != w.CacheReadTokens ||
					g.latencySum != w.latencySum || g.MaxLatencyMs != w.MaxLatencyMs || g.ttftSum != w.ttftSum ||
					g.ttftCount != w.ttftCount || g.UnpricedRequests != w.UnpricedRequests ||
					!nearRel(g.CostUSD, w.CostUSD) || !nearRel(g.SubscriptionValueUSD, w.SubscriptionValueUSD) ||
					!nearRel(g.LocalValueUSD, w.LocalValueUSD) {
					t.Fatalf("filter %d %q key %q:\n got %+v\nwant %+v", i, groupBy, key, g, w)
				}
			}
		}
	}
}

// rawOnly aggregates the window from the raw requests alone.
func rawOnly(ctx context.Context, store *Store, f Filter, dim *Dimension, byTime bool) ([]partial, error) {
	a := newAggregator(f, dim, byTime, false)
	if errRaw := scanRaw(ctx, store.DB(), a, f.From.UnixMilli(), f.To.UnixMilli()); errRaw != nil {
		return nil, errRaw
	}
	return a.partials(), nil
}

func mergeParts(parts []partial) map[string]*Totals {
	out := make(map[string]*Totals)
	for i := range parts {
		key := fmt.Sprintf("%d|%s", parts[i].bucket, parts[i].key)
		if out[key] == nil {
			out[key] = &Totals{}
		}
		out[key].add(&parts[i].totals)
	}
	return out
}

// nearRel compares costs; summing per band instead of per row only changes
// floating-point rounding.
func nearRel(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-9*(1+max(a, b))
}

func TestRollupsOutliveRawRetention(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	store, tick := openTestStore(t, Options{RetentionDays: 7, Now: func() time.Time { return now }})
	old := now.Add(-20 * 24 * time.Hour)
	for i, ts := range []time.Time{old, old.Add(time.Minute), now.Add(-time.Hour)} {
		model := "m"
		if i == 0 {
			model = "old"
		}
		store.Enqueue(Row{TS: ts.UnixMilli(), RequestID: fmt.Sprint(i), Provider: "codex", RequestedModel: model, Success: true, LatencyMs: 100, InputTokens: 10, TotalTokens: 10})
	}
	flush(t, store)
	store.SetRetentionDays(7) // unchanged: no forced prune
	store.pruneDue.Store(true)
	tick <- now
	flush(t, store)
	if n := countRows(t, store); n != 1 {
		t.Fatalf("raw rows after prune = %d, want 1", n)
	}
	ctx := context.Background()
	window := Filter{From: old.Add(-time.Hour), To: now}
	got, errSummary := store.SummaryQuery(ctx, window, "", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	if got.Totals.Requests != 3 || got.Totals.InputTokens != 30 {
		t.Fatalf("totals after raw prune = %+v", got.Totals)
	}

	store.SetRollupRetentionDays(10)
	tick <- now
	flush(t, store)
	got, _ = store.SummaryQuery(ctx, window, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("totals after rollup prune = %+v", got.Totals)
	}
	var orphans int
	if errScan := store.DB().QueryRow("SELECT COUNT(*) FROM rollup_keys WHERE requested_model = 'old'").Scan(&orphans); errScan != nil || orphans != 0 {
		t.Fatalf("orphan keys = %d, %v", orphans, errScan)
	}
	// A pruned key is recreated when it is seen again.
	store.Enqueue(Row{TS: now.UnixMilli(), RequestID: "again", Provider: "codex", RequestedModel: "old", Success: true, InputTokens: 10, TotalTokens: 10})
	flush(t, store)
	got, _ = store.SummaryQuery(ctx, Filter{From: now.Add(-2 * time.Hour), To: now.Add(time.Hour), RequestedModel: "old"}, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("recreated key totals = %+v", got.Totals)
	}
}

func TestMigrationBackfillsRollups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	ctx := context.Background()
	db, errOpen := sql.Open("sqlite", dsn(path))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	if errMigrate := applyMigration(ctx, db, 0); errMigrate != nil {
		t.Fatal(errMigrate)
	}
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rows := randomRows(base, 300, 2)
	for _, r := range rows {
		if _, errInsert := db.Exec(insertSQL,
			r.TS, r.RequestID, r.TraceID, r.Provider, r.AuthID, r.AuthLabel, r.AuthType,
			r.RequestedModel, r.UpstreamModel, r.ResponseModel, r.BaseURL,
			r.ClientKeyID, r.ClientKeyMasked, r.Endpoint, r.SessionID,
			boolInt(r.Stream), boolInt(r.Success), r.Status, r.Error, r.LatencyMs, r.TTFTMs,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens,
			r.CacheCreationTokens, r.TotalTokens, r.ReasoningEffort, r.ServiceTier); errInsert != nil {
			t.Fatal(errInsert)
		}
	}
	if errClose := db.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	store, _ := openTestStore(t, Options{Path: path, RetentionDays: -1})
	f := Filter{From: base.Add(-80 * time.Hour), To: base.Add(time.Hour), Pricer: testPricer(t)}
	got, errGot := store.aggregate(ctx, f, nil, true, false)
	want, errWant := rawOnly(ctx, store, f, nil, true)
	if errGot != nil || errWant != nil {
		t.Fatal(errGot, errWant)
	}
	g, w := mergeParts(got), mergeParts(want)
	var total int64
	for key, wt := range w {
		if g[key] == nil || g[key].Requests != wt.Requests || !nearRel(g[key].CostUSD, wt.CostUSD) || g[key].MaxLatencyMs != wt.MaxLatencyMs {
			t.Fatalf("bucket %s: got %+v want %+v", key, g[key], wt)
		}
		total += wt.Requests
	}
	if total != 300 {
		t.Fatalf("backfilled requests = %d", total)
	}
}

func TestContextBands(t *testing.T) {
	for _, tc := range []struct{ input, band, rep int64 }{
		{0, 0, 100000}, {100000, 0, 100000}, {100001, 100000, 200000},
		{272000, 200000, 272000}, {272001, 272000, 512000}, {900000, 512000, 512000 + 1_000_000_000},
	} {
		if b := contextBand(tc.input); b != tc.band || bandInput(b) != tc.rep {
			t.Errorf("input %d: band %d rep %d, want %d %d", tc.input, b, bandInput(b), tc.band, tc.rep)
		}
	}
}

// TestDatedPricesThroughRollups: a Spark model's price change applies only to
// requests from its effective time; the earlier model keeps its own price.
func TestDatedPricesThroughRollups(t *testing.T) {
	swap := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	in, out, in2, out2 := 0.5, 0.75, 1.0, 2.0
	pricer := pricing.NewPricer(nil, config.UsagePricingConfig{Models: []config.UsagePriceModel{
		{Model: "gpt-oss-120b", Input: &in, Output: &out},
		{Model: "gpt-oss-120b", Input: &in2, Output: &out2, From: swap.Format(time.RFC3339)},
		{Model: "gpt-oss-2", Input: &in2, Output: &out2},
	}})
	store, _ := openTestStore(t, Options{RetentionDays: -1})
	row := func(ts time.Time, model string) Row {
		return Row{TS: ts.UnixMilli(), Provider: "openai-compatible-spark", BaseURL: "http://spark.local/v1",
			RequestedModel: "DGX-SPARK", UpstreamModel: model, Success: true, InputTokens: 1_000_000, OutputTokens: 1_000_000}
	}
	store.Enqueue(row(swap.Add(-2*time.Hour), "gpt-oss-120b"))
	store.Enqueue(row(swap.Add(2*time.Hour), "gpt-oss-120b"))
	store.Enqueue(row(swap.Add(3*time.Hour), "gpt-oss-2"))
	flush(t, store)
	got, errSummary := store.SummaryQuery(context.Background(), Filter{From: swap.Add(-24 * time.Hour), To: swap.Add(24 * time.Hour), Pricer: pricer}, "upstream_model", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	costs := map[string]float64{}
	for _, g := range got.Groups {
		costs[g.Key] = g.CostUSD
	}
	if !near(costs["gpt-oss-120b"], 1.25+3) || !near(costs["gpt-oss-2"], 3) || !near(got.Totals.LocalValueUSD, 7.25) {
		t.Fatalf("costs = %+v totals %+v", costs, got.Totals)
	}
}

// TestRollupPruneKeepsCutoffBucket: the bucket containing the cutoff still
// covers retained requests, so it must survive pruning.
func TestRollupPruneKeepsCutoffBucket(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 7, 0, 0, time.UTC)
	store, tick := openTestStore(t, Options{RetentionDays: 7, RollupRetentionDays: 7, Now: func() time.Time { return now }})
	cutoff := now.Add(-7 * 24 * time.Hour)
	store.Enqueue(Row{TS: cutoff.Add(-30 * time.Minute).UnixMilli(), RequestID: "old", Provider: "codex", RequestedModel: "m", Success: true, TotalTokens: 1})
	store.Enqueue(Row{TS: cutoff.Add(3 * time.Minute).UnixMilli(), RequestID: "kept", Provider: "codex", RequestedModel: "m", Success: true, TotalTokens: 1})
	flush(t, store)
	store.pruneDue.Store(true)
	tick <- now
	flush(t, store)
	if n := countRows(t, store); n != 1 {
		t.Fatalf("raw rows = %d, want 1", n)
	}
	bucket := cutoff.Truncate(15 * time.Minute)
	got, _ := store.SummaryQuery(context.Background(), Filter{From: bucket, To: bucket.Add(time.Hour)}, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("requests in cutoff bucket = %d, want 1", got.Totals.Requests)
	}
}

// TestExpiredEdgesUseWholeBuckets: once raw rows are pruned, a partial-bucket
// window falls back to the whole rollup bucket instead of reporting nothing.
func TestExpiredEdgesUseWholeBuckets(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	store, tick := openTestStore(t, Options{RetentionDays: 7, Now: func() time.Time { return now }})
	day := now.Add(-20 * 24 * time.Hour)
	store.Enqueue(Row{TS: day.Add(10 * time.Minute).UnixMilli(), RequestID: "r", Provider: "codex", RequestedModel: "m", Success: true, TotalTokens: 1})
	store.Enqueue(Row{TS: now.Add(-time.Hour).UnixMilli(), RequestID: "recent", Provider: "codex", RequestedModel: "m", Success: true, TotalTokens: 1})
	flush(t, store)
	store.pruneDue.Store(true)
	tick <- now
	flush(t, store)
	if n := countRows(t, store); n != 1 {
		t.Fatalf("raw rows = %d, want 1", n)
	}
	window := Filter{From: day.Add(5 * time.Minute), To: day.Add(20 * time.Minute)}
	got, _ := store.SummaryQuery(context.Background(), window, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("requests = %d, want 1", got.Totals.Requests)
	}
	// Raising (or disabling) the retention afterwards does not bring the
	// pruned raw rows back, so the edge must keep using the rollups.
	store.SetRetentionDays(-1)
	got, _ = store.SummaryQuery(context.Background(), window, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("requests after raising retention = %d, want 1", got.Totals.Requests)
	}
}

// TestRollupKeyIDsAreNotReused: pruned key IDs are never handed out again,
// so a reader can never attribute a rollup to a different key.
func TestRollupKeyIDsAreNotReused(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	store, tick := openTestStore(t, Options{RetentionDays: 7, RollupRetentionDays: 7, Now: func() time.Time { return now }})
	store.Enqueue(Row{TS: now.Add(-20 * 24 * time.Hour).UnixMilli(), RequestID: "old", Provider: "a", RequestedModel: "m", Success: true})
	flush(t, store)
	var oldID int64
	if errScan := store.DB().QueryRow("SELECT MAX(id) FROM rollup_keys").Scan(&oldID); errScan != nil {
		t.Fatal(errScan)
	}
	store.pruneDue.Store(true)
	tick <- now
	flush(t, store)
	store.Enqueue(Row{TS: now.UnixMilli(), RequestID: "new", Provider: "b", RequestedModel: "m", Success: true})
	flush(t, store)
	var newID int64
	if errScan := store.DB().QueryRow("SELECT id FROM rollup_keys WHERE provider = 'b'").Scan(&newID); errScan != nil {
		t.Fatal(errScan)
	}
	if newID <= oldID {
		t.Fatalf("key id %d reused (old max %d)", newID, oldID)
	}
}

// TestFreshStoreEdgesStayExact: without any pruning, partial-bucket windows
// read raw rows exactly instead of widening to whole buckets.
func TestFreshStoreEdgesStayExact(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, Options{RetentionDays: 7})
	store.Enqueue(Row{TS: base.Add(10 * time.Minute).UnixMilli(), RequestID: "r", Provider: "codex", RequestedModel: "m", Success: true})
	flush(t, store)
	got, _ := store.SummaryQuery(context.Background(), Filter{From: base.Add(5 * time.Minute), To: base.Add(6 * time.Minute)}, "", time.UTC)
	if got.Totals.Requests != 0 {
		t.Fatalf("requests in [12:05,12:06) = %d, want 0", got.Totals.Requests)
	}
}

// TestRawWatermarkIsMonotonic: a later prune with a longer retention (an
// earlier cutoff) must not lower the watermark below already pruned rows.
func TestRawWatermarkIsMonotonic(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	store, tick := openTestStore(t, Options{RetentionDays: 7, Now: func() time.Time { return now }})
	day := now.Add(-20 * 24 * time.Hour)
	store.Enqueue(Row{TS: day.Add(10 * time.Minute).UnixMilli(), RequestID: "r", Provider: "codex", RequestedModel: "m", Success: true})
	flush(t, store)
	store.pruneDue.Store(true)
	tick <- now
	flush(t, store)
	store.SetRetentionDays(30)
	tick <- now
	flush(t, store)
	var watermark int64
	if errScan := store.DB().QueryRow("SELECT value FROM usage_meta WHERE key = ?", rawPrunedBeforeKey).Scan(&watermark); errScan != nil {
		t.Fatal(errScan)
	}
	if want := now.Add(-7 * 24 * time.Hour).UnixMilli(); watermark != want {
		t.Fatalf("watermark = %d, want %d", watermark, want)
	}
	got, _ := store.SummaryQuery(context.Background(), Filter{From: day.Add(5 * time.Minute), To: day.Add(20 * time.Minute)}, "", time.UTC)
	if got.Totals.Requests != 1 {
		t.Fatalf("requests = %d, want 1", got.Totals.Requests)
	}
}
