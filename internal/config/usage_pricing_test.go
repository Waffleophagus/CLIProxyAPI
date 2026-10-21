package config

import "testing"

const pricingYAML = `
observability:
  usage:
    enabled: true
    pricing:
      models:
        - model: DGX-SPARK
          price-as: zai/glm-5.3-flash
        - model: my-finetune
          input: 0.5
          output: 1.5
      subscriptions:
        - account: claude-me@example.com.json
          provider: claude
          monthly-usd: 200
      provider-classes:
        openai-compatible-dgx-spark: local
`

func TestUsagePricingConfigParses(t *testing.T) {
	if errValidate := ValidateV8Config([]byte(pricingYAML)); errValidate != nil {
		t.Fatalf("v8 validation: %v", errValidate)
	}
	cfg, errParse := ParseConfigBytes([]byte(pricingYAML))
	if errParse != nil {
		t.Fatal(errParse)
	}
	p := cfg.UsageTracking.Pricing
	if !p.IsEnabled() || p.EffectiveSource() != DefaultUsagePricingSource || len(p.Models) != 2 || p.Models[0].PriceAs != "zai/glm-5.3-flash" ||
		*p.Models[1].Output != 1.5 || p.Subscriptions[0].MonthlyUSD != 200 || p.ProviderClasses["openai-compatible-dgx-spark"] != UsageCostClassLocal {
		t.Fatalf("pricing = %+v", p)
	}
	if _, errBad := ParseConfigBytes([]byte("observability:\n  usage:\n    pricing:\n      provider-classes:\n        x: free\n")); errBad == nil {
		t.Fatal("invalid provider class accepted")
	}
	if _, errBad := ParseConfigBytes([]byte("observability:\n  usage:\n    pricing:\n      models:\n        - model: x\n")); errBad == nil {
		t.Fatal("model without prices accepted")
	}
}
