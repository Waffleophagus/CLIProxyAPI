package usagestore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedRows(t *testing.T, rows ...Row) *Store {
	t.Helper()
	store, _ := openTestStore(t, Options{RetentionDays: -1})
	for _, row := range rows {
		store.Enqueue(row)
	}
	flush(t, store)
	return store
}

func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, errLoc := time.LoadLocation(name)
	if errLoc != nil {
		t.Skipf("tzdata unavailable: %v", errLoc)
	}
	return loc
}

func TestLatencyPercentilesUseNearestRank(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	window := Filter{From: base.Add(-time.Hour), To: base}
	cases := []struct {
		latencies []int64
		p50, p95  int64
	}{
		{latencies: []int64{100, 100, 200}, p50: 100, p95: 200},
		{latencies: []int64{42}, p50: 42, p95: 42},
		{latencies: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, p50: 10, p95: 19},
		{latencies: []int64{5, 1, 4, 2, 3}, p50: 3, p95: 5},
	}
	for _, tc := range cases {
		rows := make([]Row, 0, len(tc.latencies)+1)
		for i, latency := range tc.latencies {
			rows = append(rows, Row{TS: base.Add(-time.Duration(i+1) * time.Second).UnixMilli(), Success: true, Status: 200, LatencyMs: latency})
		}
		// Failures never count toward latency percentiles.
		rows = append(rows, Row{TS: base.Add(-time.Minute).UnixMilli(), Status: 500, LatencyMs: 99999})
		store := seedRows(t, rows...)
		got, errSummary := store.SummaryQuery(context.Background(), window, "", time.UTC)
		if errSummary != nil {
			t.Fatal(errSummary)
		}
		if got.LatencyP50Ms != tc.p50 || got.LatencyP95Ms != tc.p95 {
			t.Fatalf("%v: p50 %d p95 %d, want %d %d", tc.latencies, got.LatencyP50Ms, got.LatencyP95Ms, tc.p50, tc.p95)
		}
	}
}

// America/Chicago repeats 01:00-02:00 on 2026-11-01 (CDT -05:00, then CST -06:00).
func TestHourlyBucketsKeepRepeatedDSTHourDistinct(t *testing.T) {
	chicago := loadZone(t, "America/Chicago")
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, chicago) // 05:00Z
	to := from.Add(5 * time.Hour)                       // 03:00 CST
	firstOneThirty := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	secondOneThirty := time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC)
	store := seedRows(t,
		Row{TS: firstOneThirty.UnixMilli(), Success: true, Status: 200, RequestedModel: "m", TotalTokens: 1},
		Row{TS: secondOneThirty.UnixMilli(), Success: true, Status: 200, RequestedModel: "m", TotalTokens: 2},
	)
	ctx := context.Background()
	window := Filter{From: from, To: to}

	series, errSeries := store.TimeseriesQuery(ctx, window, "hour", "", 5, chicago)
	if errSeries != nil {
		t.Fatalf("TimeseriesQuery: %v", errSeries)
	}
	want := []string{
		"2026-11-01T00:00:00.000-05:00",
		"2026-11-01T01:00:00.000-05:00",
		"2026-11-01T01:00:00.000-06:00",
		"2026-11-01T02:00:00.000-06:00",
		"2026-11-01T03:00:00.000-06:00",
	}
	if strings.Join(series.Buckets, ",") != strings.Join(want, ",") {
		t.Fatalf("buckets = %v, want %v", series.Buckets, want)
	}
	points := series.Series[0].Points
	if points[1].TotalTokens != 1 || points[2].TotalTokens != 2 {
		t.Fatalf("repeated-hour points = %+v", points)
	}

	summary, errSummary := store.SummaryQuery(ctx, window, "hour", chicago)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	if len(summary.Groups) != 2 || summary.Groups[0].Key != want[1] || summary.Groups[1].Key != want[2] ||
		summary.Groups[0].TotalTokens != 1 || summary.Groups[1].TotalTokens != 2 {
		t.Fatalf("hour groups = %+v", summary.Groups)
	}

	// A day window across the transition is 25 hours long and one bucket.
	daily, errDaily := store.TimeseriesQuery(ctx, Filter{From: from, To: from.Add(25 * time.Hour)}, "day", "", 5, chicago)
	if errDaily != nil {
		t.Fatal(errDaily)
	}
	if len(daily.Buckets) != 1 || daily.Buckets[0] != "2026-11-01T00:00:00.000-05:00" || daily.Series[0].Totals.TotalTokens != 3 {
		t.Fatalf("daily = %v %+v", daily.Buckets, daily.Series[0].Totals)
	}
}

// America/Santiago skips 2026-09-06 00:00 (clocks jump from -04:00 to -03:00),
// so that local day begins at 01:00 -03:00.
func TestDailyBucketsAcrossMissingMidnight(t *testing.T) {
	santiago := loadZone(t, "America/Santiago")
	from := time.Date(2026, 9, 4, 0, 0, 0, 0, santiago)
	to := time.Date(2026, 9, 8, 0, 0, 0, 0, santiago)
	onSixth := time.Date(2026, 9, 6, 12, 0, 0, 0, santiago)
	onFifthLate := time.Date(2026, 9, 5, 23, 30, 0, 0, santiago)
	store := seedRows(t,
		Row{TS: onFifthLate.UnixMilli(), Success: true, Status: 200, TotalTokens: 5},
		Row{TS: onSixth.UnixMilli(), Success: true, Status: 200, TotalTokens: 6},
	)
	ctx := context.Background()
	window := Filter{From: from, To: to}

	series, errSeries := store.TimeseriesQuery(ctx, window, "day", "", 5, santiago)
	if errSeries != nil {
		t.Fatalf("TimeseriesQuery: %v", errSeries)
	}
	want := []string{
		"2026-09-04T00:00:00.000-04:00",
		"2026-09-05T00:00:00.000-04:00",
		"2026-09-06T01:00:00.000-03:00",
		"2026-09-07T00:00:00.000-03:00",
	}
	if strings.Join(series.Buckets, ",") != strings.Join(want, ",") {
		t.Fatalf("buckets = %v, want %v", series.Buckets, want)
	}
	points := series.Series[0].Points
	if points[1].TotalTokens != 5 || points[2].TotalTokens != 6 {
		t.Fatalf("points = %+v", points)
	}

	summary, errSummary := store.SummaryQuery(ctx, window, "day", santiago)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	if len(summary.Groups) != 2 || summary.Groups[0].Key != want[1] || summary.Groups[1].Key != want[2] {
		t.Fatalf("day groups = %+v", summary.Groups)
	}

	// Two absolute hours from 23:30 -04:00 end at 02:30 -03:00; the hour after
	// 23:00 -04:00 is 01:00 -03:00.
	hourly, errHourly := store.TimeseriesQuery(ctx, Filter{From: onFifthLate, To: onFifthLate.Add(2 * time.Hour)}, "hour", "", 5, santiago)
	if errHourly != nil {
		t.Fatal(errHourly)
	}
	if strings.Join(hourly.Buckets, ",") != "2026-09-05T23:00:00.000-04:00,2026-09-06T01:00:00.000-03:00,2026-09-06T02:00:00.000-03:00" {
		t.Fatalf("hourly buckets = %v", hourly.Buckets)
	}
}

func TestRequestsQueryWalksPrimaryKeyWithoutSorting(t *testing.T) {
	store := seedRows(t)
	window := Filter{From: time.UnixMilli(0), To: time.UnixMilli(1 << 40)}
	filtered := window
	filtered.RequestedModel = "m"
	filtered.Credential = "auth-1"
	for _, tc := range []struct {
		f      Filter
		before int64
	}{{window, 0}, {window, 100}, {filtered, 0}, {filtered, 100}} {
		query, args := requestsSQL(tc.f, tc.before, 50)
		rows, errPlan := store.DB().Query("EXPLAIN QUERY PLAN "+query, args...)
		if errPlan != nil {
			t.Fatal(errPlan)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if errScan := rows.Scan(&id, &parent, &notUsed, &detail); errScan != nil {
				t.Fatal(errScan)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		joined := strings.Join(plan, "; ")
		if strings.Contains(joined, "TEMP B-TREE") || strings.Contains(joined, "INDEX") {
			t.Fatalf("requests plan sorts or uses a secondary index: %s", joined)
		}
	}
}
