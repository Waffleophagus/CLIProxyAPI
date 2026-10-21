// Package pricing estimates API-equivalent costs for recorded usage from the
// models.dev catalog plus user mappings, and classifies usage as paid API
// spend, subscription value, or local hardware value.
package pricing

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Rates are USD per million tokens.
type Rates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	// Reasoning prices reasoning tokens separately when non-nil; otherwise
	// they are billed as output.
	Reasoning *float64 `json:"reasoning,omitempty"`
}

// Tier replaces the base rates for requests whose input exceeds Size tokens.
type Tier struct {
	Size  int64 `json:"size"`
	Rates Rates `json:"rates"`
}

// Price is the full pricing of one catalog model.
type Price struct {
	Rates
	// Tiers are sorted by ascending Size.
	Tiers []Tier `json:"tiers,omitempty"`
	// Modes maps a response service_tier (e.g. "priority") to its rates.
	Modes map[string]Rates `json:"modes,omitempty"`
}

// ratesFor returns the rates for a request with inputTokens prompt tokens
// served at serviceTier.
func (p *Price) ratesFor(inputTokens int64, serviceTier string) Rates {
	if r, ok := p.Modes[strings.ToLower(serviceTier)]; ok {
		return r
	}
	r := p.Rates
	for _, t := range p.Tiers {
		if inputTokens > t.Size {
			r = t.Rates
		}
	}
	return r
}

// Model is one priced catalog entry.
type Model struct {
	// ID is "provider/model".
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`
	Model        string `json:"model"`
	Name         string `json:"name"`
	Price        Price  `json:"price"`
}

// Catalog indexes models.dev models by provider and model ID (lowercase).
type Catalog struct {
	byProvider map[string]map[string]*Model
	all        []*Model
}

// Len returns the number of priced models.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.all)
}

// Lookup returns the model with the given provider and model ID.
func (c *Catalog) Lookup(provider, model string) *Model {
	if c == nil {
		return nil
	}
	return c.byProvider[strings.ToLower(provider)][strings.ToLower(model)]
}

// LookupID resolves a "provider/model" ID. Model IDs may contain slashes.
func (c *Catalog) LookupID(id string) *Model {
	provider, model, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok {
		return nil
	}
	return c.Lookup(provider, model)
}

type rawCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Reasoning  *float64 `json:"reasoning"`
	Tiers      []struct {
		rawCost
		Tier struct {
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
}

type rawModel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Cost         *rawCost `json:"cost"`
	Experimental struct {
		Modes map[string]struct {
			Cost     *rawCost `json:"cost"`
			Provider struct {
				Body struct {
					ServiceTier string `json:"service_tier"`
				} `json:"body"`
			} `json:"provider"`
		} `json:"modes"`
	} `json:"experimental"`
}

type rawProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Models map[string]rawModel `json:"models"`
}

// ParseCatalog parses a models.dev api.json document. Models without input
// and output prices are skipped.
func ParseCatalog(data []byte) (*Catalog, error) {
	var raw map[string]rawProvider
	if errDecode := json.Unmarshal(data, &raw); errDecode != nil {
		return nil, fmt.Errorf("pricing: parse catalog: %w", errDecode)
	}
	c := &Catalog{byProvider: make(map[string]map[string]*Model)}
	for providerKey, p := range raw {
		providerID := strings.ToLower(firstNonEmpty(p.ID, providerKey))
		for modelKey, m := range p.Models {
			price, ok := parsePrice(m)
			if !ok {
				continue
			}
			modelID := firstNonEmpty(m.ID, modelKey)
			entry := &Model{
				ID:           providerID + "/" + modelID,
				Provider:     providerID,
				ProviderName: firstNonEmpty(p.Name, providerID),
				Model:        modelID,
				Name:         firstNonEmpty(m.Name, modelID),
				Price:        price,
			}
			if c.byProvider[providerID] == nil {
				c.byProvider[providerID] = make(map[string]*Model)
			}
			c.byProvider[providerID][strings.ToLower(modelID)] = entry
			c.all = append(c.all, entry)
		}
	}
	if len(c.all) == 0 {
		return nil, fmt.Errorf("pricing: catalog has no priced models")
	}
	sort.Slice(c.all, func(i, j int) bool { return c.all[i].ID < c.all[j].ID })
	return c, nil
}

func parsePrice(m rawModel) (Price, bool) {
	base, ok := parseRates(m.Cost, nil)
	if !ok {
		return Price{}, false
	}
	price := Price{Rates: base}
	for _, t := range m.Cost.Tiers {
		if t.Tier.Type != "context" || t.Tier.Size <= 0 {
			continue
		}
		cost := t.rawCost
		if rates, okTier := parseRates(&cost, &base); okTier {
			price.Tiers = append(price.Tiers, Tier{Size: t.Tier.Size, Rates: rates})
		}
	}
	sort.Slice(price.Tiers, func(i, j int) bool { return price.Tiers[i].Size < price.Tiers[j].Size })
	for _, mode := range m.Experimental.Modes {
		tier := strings.ToLower(strings.TrimSpace(mode.Provider.Body.ServiceTier))
		if tier == "" {
			continue
		}
		if rates, okMode := parseRates(mode.Cost, &base); okMode {
			if price.Modes == nil {
				price.Modes = make(map[string]Rates)
			}
			price.Modes[tier] = rates
		}
	}
	return price, true
}

// parseRates converts a cost block. Missing cache prices are derived from the
// base cache-to-input ratio when base is given, else from the input price.
func parseRates(c *rawCost, base *Rates) (Rates, bool) {
	if c == nil || c.Input == nil || c.Output == nil || *c.Input < 0 || *c.Output < 0 {
		return Rates{}, false
	}
	r := Rates{Input: *c.Input, Output: *c.Output, Reasoning: c.Reasoning}
	derive := func(v *float64, baseValue float64) float64 {
		if v != nil {
			return *v
		}
		if base != nil && base.Input > 0 {
			return r.Input * baseValue / base.Input
		}
		return r.Input
	}
	var baseRead, baseWrite float64
	if base != nil {
		baseRead, baseWrite = base.CacheRead, base.CacheWrite
		if r.Reasoning == nil && base.Reasoning != nil && base.Output > 0 {
			scaled := r.Output * *base.Reasoning / base.Output
			r.Reasoning = &scaled
		}
	}
	r.CacheRead = derive(c.CacheRead, baseRead)
	r.CacheWrite = derive(c.CacheWrite, baseWrite)
	return r, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// firstPartyProviders are preferred, in order, when a model ID is not found
// under the provider that served it.
var firstPartyProviders = []string{
	"anthropic", "openai", "xai", "moonshotai", "zai", "deepseek", "mistral",
	"meta", "alibaba", "minimax", "google", "cohere", "xiaomi", "stepfun",
}

// Search returns up to limit models matching every whitespace-separated term
// in query (case-insensitive, against ID and name). First-party providers and
// exact model matches rank first.
func (c *Catalog) Search(query string, limit int) []*Model {
	if c == nil || limit <= 0 {
		return nil
	}
	terms := strings.Fields(strings.ToLower(query))
	type hit struct {
		model *Model
		score int
	}
	var hits []hit
	for _, m := range c.all {
		id, name, model := strings.ToLower(m.ID), strings.ToLower(m.Name), strings.ToLower(m.Model)
		matched := true
		for _, term := range terms {
			if !strings.Contains(id, term) && !strings.Contains(name, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		score := 0
		if q := strings.Join(terms, " "); q != "" {
			switch {
			case model == q || id == q:
				score += 100
			case strings.HasPrefix(model, q):
				score += 50
			}
		}
		for i, provider := range firstPartyProviders {
			if m.Provider == provider {
				score += 40 - i
				break
			}
		}
		hits = append(hits, hit{model: m, score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]*Model, len(hits))
	for i, h := range hits {
		out[i] = h.model
	}
	return out
}
