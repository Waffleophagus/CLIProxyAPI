package management

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// hoursPerMonth prorates monthly subscription fees (average Gregorian month).
	hoursPerMonth         = 365.25 * 24 / 12
	defaultPricingWindow  = 30 * 24 * time.Hour
	defaultCatalogResults = 25
	maxCatalogResults     = 100
)

// SetPricingService sets the cost estimate service used by the usage endpoints.
func (h *Handler) SetPricingService(service *pricing.Service) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pricing = service
	h.mu.Unlock()
}

// pricingService returns the service; its methods are nil-safe.
func (h *Handler) pricingService() *pricing.Service {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pricing
}

type subscriptionFee struct {
	Account    string  `json:"account"`
	Provider   string  `json:"provider,omitempty"`
	MonthlyUSD float64 `json:"monthly_usd"`
	WindowUSD  float64 `json:"window_usd"`
}

// usagePricingWindow returns the catalog status and the subscription fees
// prorated over the filter window. Fees honor the provider and account
// filters; other filters do not narrow them.
func (h *Handler) usagePricingWindow(f usagestore.Filter) gin.H {
	h.mu.Lock()
	var subs []subscriptionFee
	if h.cfg != nil {
		for _, s := range h.cfg.UsageTracking.Pricing.Subscriptions {
			subs = append(subs, subscriptionFee{Account: strings.TrimSpace(s.Account), Provider: strings.TrimSpace(s.Provider), MonthlyUSD: s.MonthlyUSD})
		}
	}
	h.mu.Unlock()
	months := f.To.Sub(f.From).Hours() / hoursPerMonth
	fees := make([]subscriptionFee, 0, len(subs))
	var total float64
	for _, s := range subs {
		if credential := strings.TrimSpace(f.Credential); credential != "" && s.Account != credential {
			continue
		}
		if provider := strings.TrimSpace(f.Provider); provider != "" && s.Provider != "" && !strings.EqualFold(s.Provider, provider) {
			continue
		}
		s.WindowUSD = s.MonthlyUSD * months
		total += s.WindowUSD
		fees = append(fees, s)
	}
	return gin.H{
		"catalog":               h.pricingService().Status(),
		"subscription_fees_usd": total,
		"subscriptions":         fees,
	}
}

// GetUsagePricing returns the price catalog status.
func (h *Handler) GetUsagePricing(c *gin.Context) {
	c.JSON(http.StatusOK, h.pricingService().Status())
}

// RefreshUsagePricing refetches the price catalog in the background.
func (h *Handler) RefreshUsagePricing(c *gin.Context) {
	if errRefresh := h.pricingService().Refresh(); errRefresh != nil {
		status := http.StatusConflict
		if errors.Is(errRefresh, pricing.ErrRefreshTooSoon) {
			status = http.StatusTooManyRequests
		}
		c.JSON(status, gin.H{"error": errRefresh.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "refreshing"})
}

// SearchUsagePricingCatalog searches catalog models for the price mapping picker.
func (h *Handler) SearchUsagePricingCatalog(c *gin.Context) {
	limit, errLimit := intQuery(c, "limit", defaultCatalogResults, 1, maxCatalogResults)
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLimit.Error()})
		return
	}
	catalog := h.pricingService().Catalog()
	if catalog == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "price catalog is not loaded"})
		return
	}
	models := catalog.Search(c.Query("q"), limit)
	if models == nil {
		models = []*pricing.Model{}
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

type pricedModelUsage struct {
	Provider       string         `json:"provider"`
	RequestedModel string         `json:"requested_model"`
	UpstreamModel  string         `json:"upstream_model"`
	ResponseModel  string         `json:"response_model,omitempty"`
	Class          string         `json:"class"`
	PricedAs       string         `json:"priced_as,omitempty"`
	Source         string         `json:"source,omitempty"`
	Price          *pricing.Price `json:"price,omitempty"`
	// From is when the current price took effect (dated prices only).
	From time.Time `json:"from,omitzero"`
	// History lists dated prices (oldest first) when the price changed over time.
	History     []pricing.PriceVersion `json:"history,omitempty"`
	Requests    int64                  `json:"requests"`
	TotalTokens int64                  `json:"total_tokens"`
	CostUSD     float64                `json:"cost_usd"`
}

// GetUsagePricingModels lists the distinct models served in the window
// (default 30 days) and how each is priced.
func (h *Handler) GetUsagePricingModels(c *gin.Context) {
	q, errQuery := h.parseUsageQuery(c, defaultPricingWindow)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	if q.filter.Pricer == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "usage pricing is disabled"})
		return
	}
	store, ok := h.usageStore(c)
	if !ok {
		return
	}
	keys, errKeys := store.PriceKeys(c.Request.Context(), q.filter)
	if errKeys != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errKeys.Error()})
		return
	}
	out := make([]pricedModelUsage, 0, len(keys))
	for _, k := range keys {
		res := q.filter.Pricer.Resolve(k.Key)
		out = append(out, pricedModelUsage{
			Provider:       k.Key.Provider,
			RequestedModel: k.Key.RequestedModel,
			UpstreamModel:  k.Key.UpstreamModel,
			ResponseModel:  k.Key.ResponseModel,
			Class:          res.Class,
			PricedAs:       res.PricedAs,
			Source:         res.Source,
			Price:          res.Price,
			From:           res.From,
			History:        res.History,
			Requests:       k.Totals.Requests,
			TotalTokens:    k.Totals.TotalTokens,
			CostUSD:        k.Totals.CostUSD,
		})
	}
	out = mergePricedModels(out)
	c.JSON(http.StatusOK, gin.H{"models": out})
}

// mergePricedModels folds keys that resolve identically and differ only in
// the response model (empty on failures) or columns hidden from the response
// (auth type, base URL). The first, most requested, key names the row.
func mergePricedModels(in []pricedModelUsage) []pricedModelUsage {
	type mergeKey struct{ provider, requested, upstream, class, pricedAs string }
	index := make(map[mergeKey]int)
	out := in[:0]
	for _, m := range in {
		k := mergeKey{m.Provider, m.RequestedModel, m.UpstreamModel, m.Class, m.PricedAs}
		if pos, ok := index[k]; ok {
			out[pos].Requests += m.Requests
			out[pos].TotalTokens += m.TotalTokens
			out[pos].CostUSD += m.CostUSD
			continue
		}
		index[k] = len(out)
		out = append(out, m)
	}
	return out
}

type unpricedLocalModel struct {
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Alias    string `json:"alias"`
	Model    string `json:"model"`
}

// GetUnpricedLocalModels lists models configured on your own hardware
// (OpenAI-compatible providers in the local cost class, with follow-mode
// slugs resolved to the model they serve now) that have no price, so the UI
// can prompt for one when a Spark model is swapped.
func (h *Handler) GetUnpricedLocalModels(c *gin.Context) {
	pricer := h.pricingService().Pricer()
	if pricer == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "usage pricing is disabled"})
		return
	}
	h.mu.Lock()
	var cfg *config.Config
	if h.cfg != nil {
		cfg = h.cfg.CloneForRuntime()
	}
	h.mu.Unlock()
	if resolver := h.currentSlugResolver(); resolver != nil {
		cfg = resolver.Effective(cfg)
	}
	out := unpricedLocal(cfg, pricer, time.Now())
	c.JSON(http.StatusOK, gin.H{"models": out})
}

func unpricedLocal(cfg *config.Config, pricer *pricing.Pricer, now time.Time) []unpricedLocalModel {
	out := []unpricedLocalModel{}
	if cfg == nil {
		return out
	}
	seen := make(map[pricing.Key]bool)
	for _, compat := range cfg.OpenAICompatibility {
		if compat.Disabled {
			continue
		}
		provider := util.OpenAICompatibleProviderKey(compat.Name)
		for _, m := range compat.Models {
			name := strings.TrimSpace(m.Name)
			if name == "" || m.FollowPending {
				continue
			}
			alias := strings.TrimSpace(m.Alias)
			key := pricing.Key{Provider: provider, BaseURL: compat.BaseURL, RequestedModel: alias, UpstreamModel: name}
			if alias == "" {
				key.RequestedModel = name
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			res := pricer.Resolve(key)
			if res.Class != config.UsageCostClassLocal || res.At(now.UnixMilli()).Price != nil {
				continue
			}
			out = append(out, unpricedLocalModel{Provider: provider, Group: compat.Name, Alias: alias, Model: name})
		}
	}
	return out
}
