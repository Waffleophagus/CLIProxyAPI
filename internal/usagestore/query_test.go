package usagestore

import (
	"context"
	"testing"
	"time"
)

// seedQueryStore writes a fixed data set around base (UTC):
//
//	base-30h  model a / provider p1 / auth-1 / key k1   success  latency 100  tokens 10
//	base-2h   model a / provider p1 / auth-1 / key k1   success  latency 300  tokens 20
//	base-90m  model b / provider p2 / auth-2 / key k2   failure  latency 50   tokens 0
//	base-30m  model b / provider p2 / auth-2 / key k1   success  latency 200  tokens 40 (ttft 80)
//	base-10m  model c / provider p1 / auth-1 / key k2   success  latency 400  tokens 5
func seedQueryStore(t *testing.T, base time.Time) *Store {
	t.Helper()
	store, _ := openTestStore(t, Options{RetentionDays: -1})
	k1ID, k1Masked := ClientKeyIdentity("sk-client-one-000000001111")
	k2ID, k2Masked := ClientKeyIdentity("sk-client-two-000000002222")
	rows := []Row{
		{TS: base.Add(-30 * time.Hour).UnixMilli(), RequestedModel: "a", UpstreamModel: "ua", Provider: "p1", AuthID: "auth-1", AuthLabel: "one@example.com", ClientKeyID: k1ID, ClientKeyMasked: k1Masked, Success: true, Status: 200, LatencyMs: 100, InputTokens: 6, OutputTokens: 4, TotalTokens: 10},
		{TS: base.Add(-2 * time.Hour).UnixMilli(), RequestedModel: "a", UpstreamModel: "ua", Provider: "p1", AuthID: "auth-1", AuthLabel: "one@example.com", ClientKeyID: k1ID, ClientKeyMasked: k1Masked, Success: true, Status: 200, LatencyMs: 300, InputTokens: 15, OutputTokens: 5, TotalTokens: 20},
		{TS: base.Add(-90 * time.Minute).UnixMilli(), RequestedModel: "b", UpstreamModel: "ub", Provider: "p2", AuthID: "auth-2", AuthLabel: "two", ClientKeyID: k2ID, ClientKeyMasked: k2Masked, Success: false, Status: 429, Error: "rate limited", LatencyMs: 50},
		{TS: base.Add(-30 * time.Minute).UnixMilli(), RequestedModel: "b", UpstreamModel: "ub", Provider: "p2", AuthID: "auth-2", AuthLabel: "two", ClientKeyID: k1ID, ClientKeyMasked: k1Masked, Success: true, Status: 200, LatencyMs: 200, TTFTMs: 80, InputTokens: 30, OutputTokens: 10, ReasoningTokens: 4, CacheReadTokens: 12, TotalTokens: 40},
		{TS: base.Add(-10 * time.Minute).UnixMilli(), RequestedModel: "c", UpstreamModel: "uc", Provider: "p1", AuthID: "auth-1", AuthLabel: "one@example.com", ClientKeyID: k2ID, ClientKeyMasked: k2Masked, Success: true, Status: 200, LatencyMs: 400, InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}
	for _, row := range rows {
		store.Enqueue(row)
	}
	flush(t, store)
	return store
}

func last24h(base time.Time) Filter {
	return Filter{From: base.Add(-24 * time.Hour), To: base}
}

func TestSummaryTotalsAndPercentiles(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := seedQueryStore(t, base)
	got, errSummary := store.SummaryQuery(context.Background(), last24h(base), "", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	tot := got.Totals
	if tot.Requests != 4 || tot.Successes != 3 || tot.Failures != 1 {
		t.Fatalf("counts = %+v", tot)
	}
	if tot.TotalTokens != 65 || tot.InputTokens != 48 || tot.OutputTokens != 17 || tot.ReasoningTokens != 4 || tot.CacheReadTokens != 12 {
		t.Fatalf("tokens = %+v", tot)
	}
	if tot.AvgLatencyMs != 300 || tot.MaxLatencyMs != 400 || tot.AvgTTFTMs != 80 {
		t.Fatalf("latency = avg %v max %v ttft %v", tot.AvgLatencyMs, tot.MaxLatencyMs, tot.AvgTTFTMs)
	}
	if got.LatencyP50Ms != 300 || got.LatencyP95Ms != 400 { // nearest rank over [200 300 400]
		t.Fatalf("percentiles = p50 %d p95 %d", got.LatencyP50Ms, got.LatencyP95Ms)
	}
	if got.Groups != nil {
		t.Fatalf("ungrouped summary has groups: %+v", got.Groups)
	}
}

func TestSummaryGroupings(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := seedQueryStore(t, base)
	ctx := context.Background()

	byModel, errModel := store.SummaryQuery(ctx, last24h(base), "model", time.UTC)
	if errModel != nil {
		t.Fatal(errModel)
	}
	if len(byModel.Groups) != 3 || byModel.Groups[0].Key != "b" || byModel.Groups[0].Requests != 2 || byModel.Groups[0].Failures != 1 {
		t.Fatalf("model groups = %+v", byModel.Groups)
	}

	byCredential, _ := store.SummaryQuery(ctx, last24h(base), "credential", time.UTC)
	labels := map[string]string{}
	for _, g := range byCredential.Groups {
		labels[g.Key] = g.Label
	}
	if labels["auth-1"] != "one@example.com" || labels["auth-2"] != "two" {
		t.Fatalf("credential labels = %+v", labels)
	}

	byKey, _ := store.SummaryQuery(ctx, last24h(base), "api_key", time.UTC)
	k1ID, k1Masked := ClientKeyIdentity("sk-client-one-000000001111")
	if byKey.Groups[0].Key != k1ID || byKey.Groups[0].Label != k1Masked || byKey.Groups[0].Requests != 2 {
		t.Fatalf("api_key groups = %+v", byKey.Groups)
	}

	byUpstream, _ := store.SummaryQuery(ctx, last24h(base), "upstream_model", time.UTC)
	if len(byUpstream.Groups) != 3 || byUpstream.Groups[0].Key != "ub" {
		t.Fatalf("upstream groups = %+v", byUpstream.Groups)
	}

	// The 48h window includes the 30h-old row; day groups are chronological.
	wide := Filter{From: base.Add(-48 * time.Hour), To: base}
	byDay, _ := store.SummaryQuery(ctx, wide, "day", time.UTC)
	if len(byDay.Groups) != 2 || byDay.Groups[0].Key != "2026-04-30T00:00:00.000Z" || byDay.Groups[0].Requests != 1 || byDay.Groups[1].Requests != 4 {
		t.Fatalf("day groups = %+v", byDay.Groups)
	}

	// In UTC+14 (Pacific/Kiritimati) the recent rows fall on the next local day.
	kiritimati, errLoc := time.LoadLocation("Pacific/Kiritimati")
	if errLoc != nil {
		t.Skipf("tzdata unavailable: %v", errLoc)
	}
	byLocalDay, _ := store.SummaryQuery(ctx, wide, "day", kiritimati)
	if len(byLocalDay.Groups) != 2 || byLocalDay.Groups[0].Key != "2026-04-30T00:00:00.000+14:00" || byLocalDay.Groups[1].Key != "2026-05-02T00:00:00.000+14:00" {
		t.Fatalf("local day groups = %+v", byLocalDay.Groups)
	}

	byHour, _ := store.SummaryQuery(ctx, last24h(base), "hour", time.UTC)
	if len(byHour.Groups) != 2 || byHour.Groups[0].Key != "2026-05-01T10:00:00.000Z" || byHour.Groups[0].Failures != 1 || byHour.Groups[1].Requests != 2 {
		t.Fatalf("hour groups = %+v", byHour.Groups)
	}
}

func TestSummaryFilters(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := seedQueryStore(t, base)
	failed := false
	f := last24h(base)
	f.Success = &failed
	got, _ := store.SummaryQuery(context.Background(), f, "", time.UTC)
	if got.Totals.Requests != 1 || got.Totals.Failures != 1 || got.LatencyP50Ms != 0 {
		t.Fatalf("failure filter = %+v", got)
	}
	f = last24h(base)
	f.Provider = "p1"
	f.RequestedModel = "a"
	got, _ = store.SummaryQuery(context.Background(), f, "", time.UTC)
	if got.Totals.Requests != 1 || got.Totals.TotalTokens != 20 {
		t.Fatalf("provider+model filter = %+v", got.Totals)
	}
}

func TestTimeseriesBucketsAndTopN(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := seedQueryStore(t, base)
	ctx := context.Background()
	f := Filter{From: base.Add(-3 * time.Hour), To: base}

	total, errSeries := store.TimeseriesQuery(ctx, f, "hour", "", 8, time.UTC)
	if errSeries != nil {
		t.Fatal(errSeries)
	}
	if len(total.Buckets) != 3 || total.Buckets[0] != "2026-05-01T09:00:00.000Z" {
		t.Fatalf("buckets = %v", total.Buckets)
	}
	if len(total.Series) != 1 || total.Series[0].Key != "total" {
		t.Fatalf("series = %+v", total.Series)
	}
	points := total.Series[0].Points
	if points[0].Requests != 0 || points[1].Requests != 2 || points[1].Failures != 1 || points[2].Requests != 2 || points[2].T != "2026-05-01T11:00:00.000Z" {
		t.Fatalf("points = %+v", points)
	}

	byModel, _ := store.TimeseriesQuery(ctx, f, "hour", "model", 1, time.UTC)
	if len(byModel.Series) != 2 || byModel.Series[0].Key != "b" || byModel.Series[1].Key != OtherSeriesKey {
		t.Fatalf("top-1 series = %+v", byModel.Series)
	}
	if byModel.Series[0].Totals.Requests != 2 || byModel.Series[1].Totals.Requests != 2 {
		t.Fatalf("series totals = %+v / %+v", byModel.Series[0].Totals, byModel.Series[1].Totals)
	}

	daily, _ := store.TimeseriesQuery(ctx, Filter{From: base.Add(-48 * time.Hour), To: base}, "day", "", 8, time.UTC)
	if len(daily.Buckets) != 3 || daily.Series[0].Points[1].Requests != 1 || daily.Series[0].Points[2].Requests != 4 {
		t.Fatalf("daily = %v %+v", daily.Buckets, daily.Series[0].Points)
	}

	if _, errTooMany := store.TimeseriesQuery(ctx, Filter{From: base.AddDate(-2, 0, 0), To: base}, "hour", "", 8, time.UTC); errTooMany == nil {
		t.Fatal("expected too-many-buckets error")
	}
}

func TestRequestsPagination(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := seedQueryStore(t, base)
	ctx := context.Background()
	open := Filter{From: time.UnixMilli(0), To: base.Add(time.Hour)}

	page1, next, errPage := store.RequestsQuery(ctx, open, 0, 2)
	if errPage != nil {
		t.Fatal(errPage)
	}
	if len(page1) != 2 || page1[0].RequestedModel != "c" || page1[1].RequestedModel != "b" || next != page1[1].ID {
		t.Fatalf("page1 = %+v next=%d", page1, next)
	}
	page2, next, _ := store.RequestsQuery(ctx, open, next, 2)
	if len(page2) != 2 || page2[0].Success || page2[0].Status != 429 || page2[0].Error != "rate limited" {
		t.Fatalf("page2 = %+v", page2)
	}
	page3, next, _ := store.RequestsQuery(ctx, open, next, 2)
	if len(page3) != 1 || next != 0 || page3[0].Time != "2026-04-30T06:00:00.000Z" {
		t.Fatalf("page3 = %+v next=%d", page3, next)
	}

	f := open
	f.Credential = "auth-2"
	filtered, _, _ := store.RequestsQuery(ctx, f, 0, 10)
	if len(filtered) != 2 {
		t.Fatalf("credential filter rows = %d", len(filtered))
	}
}
