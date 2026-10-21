package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
)

func TestUnpricedLocal(t *testing.T) {
	in, out := 0.5, 0.75
	pricer := pricing.NewPricer(nil, config.UsagePricingConfig{Models: []config.UsagePriceModel{
		{Model: "gpt-oss-120b", Input: &in, Output: &out},
	}})
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "dgx-spark", BaseURL: "http://spark.local:8000/v1", Models: []config.OpenAICompatibilityModel{
			{Alias: "DGX-SPARK", Name: "gpt-oss-2"},
			{Alias: "OLD", Name: "gpt-oss-120b"},
			{Alias: "PENDING", Name: "PENDING", Follow: true, FollowPending: true},
		}},
		{Name: "cloud", BaseURL: "https://api.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "mystery"}}},
		{Name: "off", BaseURL: "http://127.0.0.1:1/v1", Disabled: true, Models: []config.OpenAICompatibilityModel{{Name: "x"}}},
	}}
	got := unpricedLocal(cfg, pricer, time.Now())
	if len(got) != 1 || got[0].Model != "gpt-oss-2" || got[0].Alias != "DGX-SPARK" || got[0].Provider != "openai-compatible-dgx-spark" {
		t.Fatalf("unpriced = %+v", got)
	}
}
