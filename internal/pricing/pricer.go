package pricing

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Key identifies what was served; it carries every column that affects the
// price or the cost class of a usage row.
type Key struct {
	Provider       string
	AuthType       string
	BaseURL        string
	RequestedModel string
	UpstreamModel  string
	ResponseModel  string
	ServiceTier    string
}

// Tokens are summed token counts. Input includes cache reads and writes;
// Output includes Reasoning (the usage store's accounting).
type Tokens struct {
	Input      int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
}

// Resolution sources.
const (
	SourceAuto     = "auto"
	SourceOverride = "override"
)

// PriceVersion is a price in effect from From (zero: from the beginning).
type PriceVersion struct {
	From time.Time `json:"from,omitzero"`
	// PricedAs is the models.dev ID used, "custom" for explicit prices, or
	// empty when unpriced.
	PricedAs string `json:"priced_as,omitempty"`
	// Source is "auto" or "override"; empty when unpriced.
	Source string `json:"source,omitempty"`
	// Price is nil when unpriced.
	Price *Price `json:"price,omitempty"`
}

// Resolution is how a Key is priced.
type Resolution struct {
	// Class is the cost class (config.UsageCostClass*).
	Class string `json:"class"`
	// PriceVersion is the current (latest) price.
	PriceVersion
	// History lists every price, oldest first, when the price changed over
	// time (dated mappings); empty otherwise.
	History []PriceVersion `json:"history,omitempty"`
}

// At returns the price in effect at ms (unix milliseconds).
func (r *Resolution) At(ms int64) PriceVersion {
	if len(r.History) == 0 {
		return r.PriceVersion
	}
	var current PriceVersion
	for _, v := range r.History {
		if !v.From.IsZero() && v.From.UnixMilli() > ms {
			break
		}
		current = v
	}
	return current
}

// Cost prices summed tokens served at ms. inputTokens is the per-request
// input size used to pick a context tier. The bool reports whether a price
// was in effect.
func (r *Resolution) Cost(ms, inputTokens int64, serviceTier string, t Tokens) (float64, bool) {
	v := r.At(ms)
	if v.Price == nil {
		return 0, false
	}
	return costOf(v.Price.ratesFor(inputTokens, serviceTier), t), true
}

// Pricer is an immutable snapshot of the catalog and pricing config. It is
// safe for concurrent use.
type Pricer struct {
	catalog   *Catalog
	overrides map[string][]config.UsagePriceModel
	classes   map[string]string
	bareIndex map[string]*Model

	mu    sync.Mutex
	cache map[Key]Resolution
}

// NewPricer builds a pricer. catalog may be nil (only explicit prices apply).
func NewPricer(catalog *Catalog, cfg config.UsagePricingConfig) *Pricer {
	p := &Pricer{
		catalog:   catalog,
		overrides: make(map[string][]config.UsagePriceModel),
		classes:   make(map[string]string),
		cache:     make(map[Key]Resolution),
	}
	for _, m := range cfg.Models {
		if model := strings.ToLower(strings.TrimSpace(m.Model)); model != "" {
			p.overrides[model] = append(p.overrides[model], m)
		}
	}
	for _, entries := range p.overrides {
		sort.SliceStable(entries, func(i, j int) bool {
			a, _ := entries[i].EffectiveFrom()
			b, _ := entries[j].EffectiveFrom()
			return a.Before(b)
		})
	}
	for provider, class := range cfg.ProviderClasses {
		p.classes[strings.ToLower(strings.TrimSpace(provider))] = class
	}
	p.bareIndex = buildBareIndex(catalog)
	return p
}

// buildBareIndex maps model IDs to a single entry when every provider that
// lists the ID agrees on the base input and output price. Free listings
// (local runtimes such as LM Studio) are not API prices and are skipped.
func buildBareIndex(c *Catalog) map[string]*Model {
	out := make(map[string]*Model)
	if c == nil {
		return out
	}
	conflict := make(map[string]bool)
	for _, m := range c.all {
		if m.Price.Input == 0 && m.Price.Output == 0 {
			continue
		}
		id := strings.ToLower(m.Model)
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if conflict[id] {
			continue
		}
		if prev, ok := out[id]; ok {
			if prev.Price.Input != m.Price.Input || prev.Price.Output != m.Price.Output {
				delete(out, id)
				conflict[id] = true
			}
			continue
		}
		out[id] = m
	}
	return out
}

// CatalogSize returns the number of priced catalog models.
func (p *Pricer) CatalogSize() int {
	if p == nil {
		return 0
	}
	return p.catalog.Len()
}

// Cost prices summed tokens for key served at ms (unix milliseconds).
// inputTokens is the per-request input size used to pick a context tier. It
// returns the USD cost, the cost class and whether a price was in effect.
func (p *Pricer) Cost(key Key, ms, inputTokens int64, t Tokens) (float64, string, bool) {
	res := p.Resolve(key)
	usd, priced := res.Cost(ms, inputTokens, key.ServiceTier, t)
	return usd, res.Class, priced
}

func costOf(r Rates, t Tokens) float64 {
	uncached := max(0, t.Input-t.CacheRead-t.CacheWrite)
	output := float64(t.Output) * r.Output
	if r.Reasoning != nil {
		reasoning := min(max(0, t.Reasoning), t.Output)
		output = float64(t.Output-reasoning)*r.Output + float64(reasoning)**r.Reasoning
	}
	return (float64(uncached)*r.Input + float64(t.CacheRead)*r.CacheRead +
		float64(t.CacheWrite)*r.CacheWrite + output) / 1e6
}

// Resolve returns the class and price for key; ServiceTier is ignored.
func (p *Pricer) Resolve(key Key) Resolution {
	if p == nil {
		return Resolution{Class: classify(nil, key)}
	}
	key.ServiceTier = ""
	p.mu.Lock()
	res, ok := p.cache[key]
	p.mu.Unlock()
	if ok {
		return res
	}
	res = p.resolve(key)
	p.mu.Lock()
	p.cache[key] = res
	p.mu.Unlock()
	return res
}

func (p *Pricer) resolve(key Key) Resolution {
	res := Resolution{Class: classify(p.classes, key)}
	// Mappings of the served model win over mappings of the requested alias,
	// so a model behind a slug keeps its own price after the slug moves on.
	for _, model := range []string{key.UpstreamModel, key.ResponseModel, key.RequestedModel} {
		for _, variant := range modelVariants(model) {
			entries, ok := p.overrides[variant]
			if !ok {
				continue
			}
			versions := make([]PriceVersion, 0, len(entries)+1)
			for _, entry := range entries {
				v := PriceVersion{Source: SourceOverride}
				v.From, _ = entry.EffectiveFrom()
				if priceAs := strings.TrimSpace(entry.PriceAs); priceAs != "" {
					if m := p.catalog.LookupID(priceAs); m != nil {
						v.PricedAs, v.Price = m.ID, &m.Price
					} else {
						v.Source = ""
					}
				} else {
					v.PricedAs, v.Price = "custom", customPrice(entry)
				}
				versions = append(versions, v)
			}
			if !versions[0].From.IsZero() {
				versions = append([]PriceVersion{p.autoPrice(key)}, versions...)
			}
			res.PriceVersion = versions[len(versions)-1]
			if len(versions) > 1 {
				res.History = versions
			}
			return res
		}
	}
	res.PriceVersion = p.autoPrice(key)
	return res
}

// autoPrice matches what was actually served against the catalog; the
// requested alias only counts when no upstream model was recorded.
func (p *Pricer) autoPrice(key Key) PriceVersion {
	if p.catalog == nil {
		return PriceVersion{}
	}
	home := providerHomes[strings.ToLower(key.Provider)]
	served := []string{key.UpstreamModel, key.ResponseModel}
	if strings.TrimSpace(key.UpstreamModel) == "" && strings.TrimSpace(key.ResponseModel) == "" {
		served = []string{key.RequestedModel}
	}
	for _, model := range served {
		for _, variant := range modelVariants(model) {
			if m := p.autoMatch(home, variant); m != nil {
				return PriceVersion{Source: SourceAuto, PricedAs: m.ID, Price: &m.Price}
			}
		}
	}
	return PriceVersion{}
}

func (p *Pricer) autoMatch(home, model string) *Model {
	if home != "" {
		if m := p.catalog.Lookup(home, model); m != nil {
			return m
		}
	}
	for _, provider := range firstPartyProviders {
		if m := p.catalog.Lookup(provider, model); m != nil {
			return m
		}
	}
	return p.bareIndex[model]
}

func customPrice(m config.UsagePriceModel) *Price {
	value := func(v *float64, fallback float64) float64 {
		if v == nil {
			return fallback
		}
		return *v
	}
	input := value(m.Input, 0)
	return &Price{Rates: Rates{
		Input:      input,
		Output:     value(m.Output, 0),
		CacheRead:  value(m.CacheRead, input),
		CacheWrite: value(m.CacheWrite, input),
	}}
}

// providerHomes maps usage provider IDs to their models.dev provider.
var providerHomes = map[string]string{
	"claude": "anthropic",
	"codex":  "openai",
	"xai":    "xai",
	"meta":   "meta",
}

var (
	dateSuffix    = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)
	bracketSuffix = regexp.MustCompile(`\s*[\[(][^\[\]()]*[\])]$`)
)

// modelVariants returns the lowercase model ID followed by progressively
// normalized forms: without a "[1m]"/"(high)" suffix, without an
// organization prefix, without a date suffix, and without "-thinking".
func modelVariants(model string) []string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	out := []string{model}
	add := func(v string) {
		if v != "" && v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	v := bracketSuffix.ReplaceAllString(model, "")
	add(v)
	if i := strings.LastIndex(v, "/"); i >= 0 {
		v = v[i+1:]
		add(v)
	}
	v = dateSuffix.ReplaceAllString(v, "")
	add(v)
	add(strings.TrimSuffix(v, "-thinking"))
	return out
}

// classify picks the cost class: an explicit provider class, then OAuth
// accounts as subscriptions, OpenAI-compatible providers on private networks
// as local hardware, and everything else as paid API usage.
func classify(classes map[string]string, key Key) string {
	provider := strings.ToLower(strings.TrimSpace(key.Provider))
	if class, ok := classes[provider]; ok {
		return class
	}
	if strings.EqualFold(key.AuthType, "oauth") {
		return config.UsageCostClassSubscription
	}
	if strings.HasPrefix(provider, "openai-compat") && isLocalURL(key.BaseURL) {
		return config.UsageCostClassLocal
	}
	return config.UsageCostClassAPI
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isLocalURL reports whether rawURL points at this machine, a private or
// Tailscale (CGNAT) address, or a LAN-only host name.
func isLocalURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, errParse := url.Parse(rawURL)
	if errParse != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home", ".internal", ".home.arpa", ".ts.net", ".localhost"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
