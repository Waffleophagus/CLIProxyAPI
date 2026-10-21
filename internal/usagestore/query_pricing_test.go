package usagestore

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
)

const pricingTestCatalog = `{
  "openai": {"id": "openai", "name": "OpenAI", "models": {
    "gpt-6-sol": {"id": "gpt-6-sol", "name": "GPT-6 Sol",
      "cost": {"input": 2, "output": 10, "cache_read": 0.2,
        "tiers": [{"input": 4, "output": 15, "cache_read": 0.4, "tier": {"type": "context", "size": 272000}}]}}
  }},
  "zai": {"id": "zai", "name": "Z.AI", "models": {
    "glm-5.3-flash": {"id": "glm-5.3-flash", "name": "GLM 5.3 Flash", "cost": {"input": 0.2, "output": 0.8}}
  }}
}`

func TestPricedQueries(t *testing.T) {
	catalog, errParse := pricing.ParseCatalog([]byte(pricingTestCatalog))
	if errParse != nil {
		t.Fatal(errParse)
	}
	pricer := pricing.NewPricer(catalog, config.UsagePricingConfig{
		Models: []config.UsagePriceModel{{Model: "DGX-SPARK", PriceAs: "zai/glm-5.3-flash"}},
	})
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, Options{RetentionDays: -1})
	rows := []Row{
		// Subscription, base tier: 100k input (40k cached), 10k output.
		{TS: base.Add(-3 * time.Hour).UnixMilli(), Provider: "codex", AuthID: "codex-a", AuthType: "oauth", RequestedModel: "gpt-6-sol", UpstreamModel: "gpt-6-sol", Success: true, InputTokens: 100_000, CacheReadTokens: 40_000, OutputTokens: 10_000, TotalTokens: 110_000},
		// Same key in the same 15-minute bucket but above the 272k tier.
		{TS: base.Add(-3 * time.Hour).UnixMilli(), Provider: "codex", AuthID: "codex-a", AuthType: "oauth", RequestedModel: "gpt-6-sol", UpstreamModel: "gpt-6-sol", Success: true, InputTokens: 300_000, OutputTokens: 10_000, TotalTokens: 310_000},
		// Local hardware priced through a slug mapping.
		{TS: base.Add(-2 * time.Hour).UnixMilli(), Provider: "openai-compatible-dgx-spark", BaseURL: "http://spark.local:8000/v1", RequestedModel: "DGX-SPARK", UpstreamModel: "glm-local", Success: true, InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
		// Paid API key, unknown model.
		{TS: base.Add(-1 * time.Hour).UnixMilli(), Provider: "openai-compatible-cloud", BaseURL: "https://api.example.com/v1", AuthType: "api_key", RequestedModel: "mystery", UpstreamModel: "mystery", Success: true, InputTokens: 10, OutputTokens: 10, TotalTokens: 20},
	}
	for _, row := range rows {
		store.Enqueue(row)
	}
	flush(t, store)
	ctx := context.Background()
	f := last24h(base)
	f.Pricer = pricer

	sub := (60_000*2.0 + 40_000*0.2 + 10_000*10.0 + 300_000*4.0 + 10_000*15.0) / 1e6
	local := 0.2 + 0.8
	got, errSummary := store.SummaryQuery(ctx, f, "model", time.UTC)
	if errSummary != nil {
		t.Fatal(errSummary)
	}
	tot := got.Totals
	if !near(tot.SubscriptionValueUSD, sub) || !near(tot.LocalValueUSD, local) || tot.APISpendUSD != 0 ||
		!near(tot.CostUSD, sub+local) || tot.UnpricedRequests != 1 || tot.Requests != 4 {
		t.Fatalf("totals = %+v (want subscription %v local %v)", tot, sub, local)
	}
	if len(got.Groups) != 3 || got.Groups[0].Key != "gpt-6-sol" || got.Groups[0].Requests != 2 || !near(got.Groups[0].CostUSD, sub) {
		t.Fatalf("groups = %+v", got.Groups)
	}

	series, errSeries := store.TimeseriesQuery(ctx, f, "hour", "", 5, time.UTC)
	if errSeries != nil {
		t.Fatal(errSeries)
	}
	if !near(series.Series[0].Totals.CostUSD, sub+local) {
		t.Fatalf("series totals = %+v", series.Series[0].Totals)
	}

	keys, errKeys := store.PriceKeys(ctx, f)
	if errKeys != nil || len(keys) != 3 || keys[0].Key.UpstreamModel != "gpt-6-sol" || keys[0].Totals.Requests != 2 {
		t.Fatalf("price keys = %+v, %v", keys, errKeys)
	}

	requests, _, errRequests := store.RequestsQuery(ctx, f, 0, 10)
	if errRequests != nil {
		t.Fatal(errRequests)
	}
	if requests[0].CostUSD != nil || requests[0].CostClass != config.UsageCostClassAPI {
		t.Fatalf("unpriced row = %+v", requests[0])
	}
	if requests[1].CostUSD == nil || !near(*requests[1].CostUSD, local) || requests[1].PricedAs != "zai/glm-5.3-flash" || requests[1].CostClass != config.UsageCostClassLocal {
		t.Fatalf("local row = %+v", requests[1])
	}

	// Without a pricer, no cost is computed.
	f.Pricer = nil
	plain, _ := store.SummaryQuery(ctx, f, "", time.UTC)
	if plain.Totals.CostUSD != 0 || plain.Totals.UnpricedRequests != 0 || plain.Totals.Requests != 4 {
		t.Fatalf("unpriced summary = %+v", plain.Totals)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
