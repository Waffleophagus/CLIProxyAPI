package pricing

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// testCatalogJSON is a trimmed models.dev document (USD per million tokens).
const testCatalogJSON = `{
  "anthropic": {"id": "anthropic", "name": "Anthropic", "models": {
    "claude-opus-5": {"id": "claude-opus-5", "name": "Claude Opus 5",
      "cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}}
  }},
  "openai": {"id": "openai", "name": "OpenAI", "models": {
    "gpt-6-sol": {"id": "gpt-6-sol", "name": "GPT-6 Sol",
      "cost": {"input": 2, "output": 10, "cache_read": 0.2,
        "tiers": [{"input": 4, "output": 15, "tier": {"type": "context", "size": 272000}}]},
      "experimental": {"modes": {"fast": {"cost": {"input": 4, "output": 20, "cache_read": 0.4},
        "provider": {"body": {"service_tier": "priority"}}}}}}
  }},
  "zai": {"id": "zai", "name": "Z.AI", "models": {
    "glm-5.3-flash": {"id": "glm-5.3-flash", "name": "GLM 5.3 Flash", "cost": {"input": 0.2, "output": 0.8}}
  }},
  "somehost": {"id": "somehost", "name": "Some Host", "models": {
    "qwen3-coder": {"id": "qwen3-coder", "name": "Qwen3 Coder", "cost": {"input": 0.3, "output": 1.2}},
    "free-thing": {"id": "free-thing", "name": "No cost"}
  }},
  "otherhost": {"id": "otherhost", "name": "Other Host", "models": {
    "qwen3-coder": {"id": "qwen3-coder", "name": "Qwen3 Coder", "cost": {"input": 0.3, "output": 1.2}},
    "split": {"id": "split", "name": "Split", "cost": {"input": 1, "output": 1}}
  }},
  "thirdhost": {"id": "thirdhost", "name": "Third Host", "models": {
    "split": {"id": "split", "name": "Split", "cost": {"input": 2, "output": 2}}
  }},
  "lmstudio": {"id": "lmstudio", "name": "LM Studio", "models": {
    "local/only-free": {"id": "local/only-free", "name": "Free local", "cost": {"input": 0, "output": 0}}
  }}
}`

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, errParse := ParseCatalog([]byte(testCatalogJSON))
	if errParse != nil {
		t.Fatal(errParse)
	}
	return c
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func ptr(v float64) *float64 { return &v }

func TestParseCatalog(t *testing.T) {
	c := testCatalog(t)
	if c.Len() != 8 || c.Lookup("somehost", "free-thing") != nil {
		t.Fatalf("models = %d, unpriced entry kept = %v", c.Len(), c.Lookup("somehost", "free-thing") != nil)
	}
	sol := c.LookupID("openai/gpt-6-sol")
	if sol == nil || len(sol.Price.Tiers) != 1 || sol.Price.Modes["priority"].Output != 20 {
		t.Fatalf("gpt-6-sol = %+v", sol)
	}
	// Missing tier cache price keeps the base cache-to-input ratio; a missing
	// cache-write price falls back to input.
	if tier := sol.Price.Tiers[0].Rates; !near(tier.CacheRead, 0.4) || !near(sol.Price.CacheWrite, 2) {
		t.Fatalf("derived cache rates = tier %+v base %+v", tier, sol.Price.Rates)
	}
}

func TestCostFormula(t *testing.T) {
	p := NewPricer(testCatalog(t), config.UsagePricingConfig{})
	key := Key{Provider: "claude", AuthType: "oauth", UpstreamModel: "claude-opus-5-20260724"}
	// 1M input of which 600k cache reads and 100k cache writes; 200k output.
	usd, class, priced := p.Cost(key, 0, 1_000_000, Tokens{Input: 1_000_000, CacheRead: 600_000, CacheWrite: 100_000, Output: 200_000, Reasoning: 50_000})
	want := 0.3*5 + 0.6*0.5 + 0.1*6.25 + 0.2*25
	if !priced || class != config.UsageCostClassSubscription || !near(usd, want) {
		t.Fatalf("cost = %v %s %v, want %v", usd, class, priced, want)
	}

	sol := Key{Provider: "codex", AuthType: "oauth", UpstreamModel: "gpt-6-sol"}
	tokens := Tokens{Input: 1_000_000, Output: 1_000_000}
	if usd, _, _ := p.Cost(sol, 0, 100_000, tokens); !near(usd, 12) {
		t.Fatalf("base tier = %v", usd)
	}
	if usd, _, _ := p.Cost(sol, 0, 300_000, tokens); !near(usd, 19) {
		t.Fatalf("long-context tier = %v", usd)
	}
	sol.ServiceTier = "priority"
	if usd, _, _ := p.Cost(sol, 0, 100_000, tokens); !near(usd, 24) {
		t.Fatalf("priority mode = %v", usd)
	}

	reasoning := &Price{Rates: Rates{Input: 1, Output: 2, Reasoning: ptr(10)}}
	if got := costOf(reasoning.Rates, Tokens{Output: 1_000_000, Reasoning: 250_000}); !near(got, 0.75*2+0.25*10) {
		t.Fatalf("separate reasoning price = %v", got)
	}
}

func TestResolve(t *testing.T) {
	cfg := config.UsagePricingConfig{
		Models: []config.UsagePriceModel{
			{Model: "DGX-SPARK", PriceAs: "zai/glm-5.3-flash"},
			{Model: "my-finetune", Input: ptr(1), Output: ptr(3)},
			{Model: "broken", PriceAs: "nobody/nothing"},
		},
		ProviderClasses: map[string]string{"openai-compatible-remote-box": config.UsageCostClassAPI},
	}
	p := NewPricer(testCatalog(t), cfg)
	cases := []struct {
		name               string
		key                Key
		pricedAs, source   string
		class              string
		wantCacheReadPrice float64
	}{
		{"dated claude id", Key{Provider: "claude", AuthType: "oauth", UpstreamModel: "claude-opus-5-20260724"}, "anthropic/claude-opus-5", SourceAuto, config.UsageCostClassSubscription, 0.5},
		{"thinking suffix", Key{Provider: "claude", AuthType: "apikey", UpstreamModel: "claude-opus-5(high)"}, "anthropic/claude-opus-5", SourceAuto, config.UsageCostClassAPI, 0.5},
		{"slug override", Key{Provider: "openai-compatible-dgx-spark", BaseURL: "http://spark.local:8000/v1", RequestedModel: "dgx-spark", UpstreamModel: "/models/GLM-5.3-Flash-EXL3"}, "zai/glm-5.3-flash", SourceOverride, config.UsageCostClassLocal, 0.2},
		{"org prefix auto", Key{Provider: "openai-compatible-box", BaseURL: "http://100.101.1.2:8000/v1", UpstreamModel: "Qwen/qwen3-coder"}, "otherhost/qwen3-coder", SourceAuto, config.UsageCostClassLocal, 0.3},
		{"custom price", Key{Provider: "openai-compatible-remote-box", BaseURL: "http://10.0.0.2", UpstreamModel: "my-finetune"}, "custom", SourceOverride, config.UsageCostClassAPI, 1},
		{"conflicting bare id", Key{Provider: "openai-compatible-x", BaseURL: "https://api.example.com", UpstreamModel: "split"}, "", "", config.UsageCostClassAPI, 0},
		{"broken override", Key{Provider: "claude", UpstreamModel: "broken"}, "", "", config.UsageCostClassAPI, 0},
		{"free listing ignored", Key{Provider: "openai-compatible-box", BaseURL: "http://127.0.0.1:1234", UpstreamModel: "only-free"}, "", "", config.UsageCostClassLocal, 0},
		{"requested fallback", Key{Provider: "xai", RequestedModel: "glm-5.3-flash"}, "zai/glm-5.3-flash", SourceAuto, config.UsageCostClassAPI, 0.2},
	}
	for _, tc := range cases {
		res := p.Resolve(tc.key)
		if res.PricedAs != tc.pricedAs || res.Source != tc.source || res.Class != tc.class {
			t.Errorf("%s: got %+v, want priced_as %q source %q class %q", tc.name, res, tc.pricedAs, tc.source, tc.class)
			continue
		}
		if tc.pricedAs != "" && !near(res.Price.CacheRead, tc.wantCacheReadPrice) {
			t.Errorf("%s: cache read price = %v", tc.name, res.Price.CacheRead)
		}
	}
	// The served model wins over the requested alias in automatic matching.
	if res := p.Resolve(Key{Provider: "codex", RequestedModel: "claude-opus-5", UpstreamModel: "gpt-6-sol"}); res.PricedAs != "openai/gpt-6-sol" {
		t.Fatalf("served model priority = %+v", res)
	}
}

func TestIsLocalURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:8000/v1":      true,
		"http://192.168.1.20/v1":        true,
		"http://100.88.1.4:8000":        true,
		"http://spark.local:8000/v1":    true,
		"http://spark:8000/v1":          true,
		"https://spark.tail1234.ts.net": true,
		"https://api.openai.com/v1":     false,
		"https://8.8.8.8/v1":            false,
		"":                              false,
	} {
		if got := isLocalURL(url); got != want {
			t.Errorf("isLocalURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestSearch(t *testing.T) {
	c := testCatalog(t)
	got := c.Search("opus", 10)
	if len(got) != 1 || got[0].ID != "anthropic/claude-opus-5" {
		t.Fatalf("search opus = %+v", got)
	}
	got = c.Search("qwen coder", 10)
	if len(got) != 2 {
		t.Fatalf("multi-term search = %+v", got)
	}
	if got = c.Search("", 3); len(got) != 3 || got[0].Provider != "anthropic" {
		t.Fatalf("empty search should rank first-party providers first: %+v", got)
	}
}

func TestServiceLoadsAndCachesCatalog(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, CacheFileName)
	fetched := make(chan struct{}, 4)
	s := NewService()
	defer s.Close()
	s.fetch = func(context.Context, string) ([]byte, error) {
		fetched <- struct{}{}
		return []byte(testCatalogJSON), nil
	}
	if s.Apply(false, config.UsagePricingConfig{}, cachePath); s.Pricer() != nil {
		t.Fatal("disabled service returned a pricer")
	}
	s.Apply(true, config.UsagePricingConfig{}, cachePath)
	if s.Pricer() == nil {
		t.Fatal("enabled service has no pricer before the catalog loads")
	}
	<-fetched
	waitFor(t, func() bool { return s.Pricer().CatalogSize() == 8 })
	waitFor(t, func() bool { _, err := os.Stat(cachePath); return err == nil })
	status := s.Status()
	if !status.Enabled || status.Source != config.DefaultUsagePricingSource || status.Models != 8 || status.FetchedAt.IsZero() {
		t.Fatalf("status = %+v", status)
	}

	// Config changes rebuild the pricer without refetching.
	s.Apply(true, config.UsagePricingConfig{Models: []config.UsagePriceModel{{Model: "x", Input: ptr(1), Output: ptr(1)}}}, cachePath)
	if res := s.Pricer().Resolve(Key{UpstreamModel: "x"}); res.PricedAs != "custom" || s.Pricer().CatalogSize() != 8 {
		t.Fatalf("rebuilt pricer = %+v", res)
	}

	// A fresh service starts from the disk cache without fetching.
	var cached cacheFile
	data, _ := os.ReadFile(cachePath)
	if errDecode := json.Unmarshal(data, &cached); errDecode != nil || cached.Source != config.DefaultUsagePricingSource {
		t.Fatalf("cache = %v %q", errDecode, cached.Source)
	}
	s2 := NewService()
	defer s2.Close()
	s2.fetch = func(context.Context, string) ([]byte, error) {
		t.Error("fresh cache should not be refetched")
		return nil, context.Canceled
	}
	s2.now = func() time.Time { return cached.FetchedAt.Add(time.Hour) }
	s2.Apply(true, config.UsagePricingConfig{}, cachePath)
	waitFor(t, func() bool { return s2.Pricer().CatalogSize() == 8 })
}

// waitFor polls an asynchronous condition (background goroutine hand-off,
// not a TTL), failing after a generous deadline.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDatedPrices(t *testing.T) {
	swap := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.UsagePricingConfig{Models: []config.UsagePriceModel{
		{Model: "gpt-oss-120b", Input: ptr(0.5), Output: ptr(0.75)},
		{Model: "gpt-oss-120b", Input: ptr(1), Output: ptr(2), From: swap.Format(time.RFC3339)},
		{Model: "fresh", PriceAs: "zai/glm-5.3-flash", From: swap.Format(time.RFC3339)},
		{Model: "DGX-SPARK", Input: ptr(9), Output: ptr(9)},
	}}
	p := NewPricer(testCatalog(t), cfg)
	tokens := Tokens{Input: 1_000_000, Output: 1_000_000}
	key := Key{Provider: "openai-compatible-spark", BaseURL: "http://spark.local", RequestedModel: "DGX-SPARK", UpstreamModel: "gpt-oss-120b"}
	before, after := swap.Add(-time.Hour).UnixMilli(), swap.Add(time.Hour).UnixMilli()
	if usd, _, _ := p.Cost(key, before, 0, tokens); !near(usd, 1.25) {
		t.Fatalf("before swap = %v", usd)
	}
	if usd, _, _ := p.Cost(key, after, 0, tokens); !near(usd, 3) {
		t.Fatalf("after swap = %v", usd)
	}
	res := p.Resolve(key)
	if len(res.History) != 2 || res.Price.Input != 1 || !res.From.Equal(swap) {
		t.Fatalf("resolution = %+v", res)
	}
	// A model first priced from a date is unpriced (no automatic match) before it.
	fresh := Key{Provider: "openai-compatible-spark", BaseURL: "http://spark.local", UpstreamModel: "fresh"}
	if _, _, priced := p.Cost(fresh, before, 0, tokens); priced {
		t.Fatal("dated price applied before its start")
	}
	if usd, _, _ := p.Cost(fresh, after, 0, tokens); !near(usd, 1) {
		t.Fatalf("fresh after = %v", usd)
	}
	// The alias mapping only applies to served models without their own price.
	other := Key{Provider: "openai-compatible-spark", RequestedModel: "DGX-SPARK", UpstreamModel: "unknown-model"}
	if usd, _, _ := p.Cost(other, after, 0, tokens); !near(usd, 18) {
		t.Fatalf("alias fallback = %v", usd)
	}
}
