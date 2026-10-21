package usagestore

import (
	"context"
	"testing"
	"time"
)

func TestGenWindowQualification(t *testing.T) {
	ok := Row{Success: true, Stream: true, OutputTokens: 100, LatencyMs: 2500, TTFTMs: 500}
	if w := genWindowMs(&ok); w != 2000 {
		t.Fatalf("qualifying window = %d, want 2000", w)
	}
	cases := map[string]func(r *Row){
		"failed":          func(r *Row) { r.Success = false },
		"not streamed":    func(r *Row) { r.Stream = false },
		"no output":       func(r *Row) { r.OutputTokens = 0 },
		"no ttft":         func(r *Row) { r.TTFTMs = 0 },
		"buffered output": func(r *Row) { r.TTFTMs = r.LatencyMs - (minGenWindowMs - 1) },
	}
	for name, mutate := range cases {
		r := ok
		mutate(&r)
		if w := genWindowMs(&r); w != 0 {
			t.Fatalf("%s: window = %d, want 0", name, w)
		}
	}
	edge := ok
	edge.TTFTMs = edge.LatencyMs - minGenWindowMs
	if w := genWindowMs(&edge); w != minGenWindowMs {
		t.Fatalf("minimum window = %d, want %d", w, minGenWindowMs)
	}
}

// TestOutputTPS checks that speed weighs requests by their generation
// window and ignores rows that do not qualify, in summaries, groups and
// request rows alike.
func TestOutputTPS(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 7, 0, 0, time.UTC)
	store, _ := openTestStore(t, Options{RetentionDays: -1})
	row := func(ago time.Duration, model string, stream bool, latency, ttft, output int64) Row {
		return Row{TS: base.Add(-ago).UnixMilli(), Provider: "openai-compatible-spark", RequestedModel: model,
			UpstreamModel: model, Success: true, Status: 200, Stream: stream, LatencyMs: latency, TTFTMs: ttft,
			OutputTokens: output, TotalTokens: output}
	}
	for _, r := range []Row{
		row(3*time.Hour, "spark", true, 11_000, 1_000, 300),   // 30 tok/s over 10 s
		row(2*time.Hour, "spark", true, 3_000, 1_000, 100),    // 50 tok/s over 2 s
		row(90*time.Minute, "spark", true, 5_002, 5_000, 900), // buffered: ignored
		row(time.Hour, "spark", false, 4_000, 3_990, 200),     // not streamed: ignored
		row(30*time.Minute, "other", true, 1_500, 500, 80),    // 80 tok/s over 1 s
	} {
		store.Enqueue(r)
	}
	flush(t, store)
	ctx := context.Background()
	f := Filter{From: base.Add(-24 * time.Hour), To: base}

	got, errSummary := store.SummaryQuery(ctx, f, "model", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	// (300 + 100 + 80) tokens over (10 + 2 + 1) s.
	if want := round1(480.0 / 13); got.Totals.OutputTPS != want {
		t.Fatalf("total tps = %v, want %v", got.Totals.OutputTPS, want)
	}
	byModel := map[string]float64{}
	for _, g := range got.Groups {
		byModel[g.Key] = g.OutputTPS
	}
	// Window-weighted (400 / 12 s), not the mean of 30 and 50.
	if want := round1(400.0 / 12); byModel["spark"] != want || byModel["other"] != 80 {
		t.Fatalf("group tps = %v, want spark %v other 80", byModel, want)
	}

	none, _ := store.SummaryQuery(ctx, Filter{From: base.Add(-80 * time.Minute), To: base.Add(-40 * time.Minute)}, "", time.UTC)
	if none.Totals.Requests != 1 || none.Totals.OutputTPS != 0 {
		t.Fatalf("window without a qualifying stream = %+v", none.Totals)
	}

	rows, _, errRows := store.RequestsQuery(ctx, f, 0, 10)
	if errRows != nil {
		t.Fatal(errRows)
	}
	var speeds []float64
	for _, r := range rows {
		if r.OutputTPS != nil {
			speeds = append(speeds, *r.OutputTPS)
		}
	}
	if len(speeds) != 3 || speeds[0] != 80 || speeds[1] != 50 || speeds[2] != 30 {
		t.Fatalf("request speeds = %v", speeds)
	}
}
