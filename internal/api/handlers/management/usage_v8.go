package management

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestore"
)

const (
	defaultUsageWindow   = 24 * time.Hour
	defaultUsageTop      = 8
	maxUsageTop          = 50
	defaultRequestsLimit = 50
	maxRequestsLimit     = 500
)

// farFuture bounds open-ended request listings.
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// SetUsageRuntime sets the persistent usage store used by the usage endpoints.
func (h *Handler) SetUsageRuntime(runtime *usagestore.Runtime) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usageRuntime = runtime
	h.mu.Unlock()
}

// usageStore returns the active store or writes an explanatory error response.
func (h *Handler) usageStore(c *gin.Context) (*usagestore.Store, bool) {
	h.mu.Lock()
	runtime := h.usageRuntime
	h.mu.Unlock()
	store, errStore := runtime.Store()
	if errStore == nil {
		return store, true
	}
	if errors.Is(errStore, usagestore.ErrDisabled) {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "usage tracking is disabled",
			"message": "Enable it with observability.usage.enabled: true in the config.",
		})
		return nil, false
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":   "usage store unavailable",
		"message": errStore.Error(),
	})
	return nil, false
}

type usageQuery struct {
	filter usagestore.Filter
	loc    *time.Location
}

// parseUsageTime accepts RFC3339 timestamps or unix milliseconds.
func parseUsageTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if ms, errInt := strconv.ParseInt(raw, 10, 64); errInt == nil {
		return time.UnixMilli(ms), nil
	}
	t, errParse := time.Parse(time.RFC3339Nano, raw)
	if errParse != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: use RFC3339 or unix milliseconds", raw)
	}
	return t, nil
}

// parseUsageQuery reads the shared window, timezone, and filter parameters.
// When defaultWindow is zero, a missing from/to leaves the window open.
func parseUsageQuery(c *gin.Context, defaultWindow time.Duration) (usageQuery, error) {
	q := usageQuery{loc: time.Local}
	if tz := strings.TrimSpace(c.Query("tz")); tz != "" {
		loc, errLoc := time.LoadLocation(tz)
		if errLoc != nil {
			return q, fmt.Errorf("invalid tz %q", tz)
		}
		q.loc = loc
	}
	now := time.Now()
	to, from := now, time.Time{}
	if defaultWindow == 0 {
		to = farFuture
		from = time.UnixMilli(0)
	}
	if raw := c.Query("to"); strings.TrimSpace(raw) != "" {
		parsed, errParse := parseUsageTime(raw)
		if errParse != nil {
			return q, errParse
		}
		to = parsed
	}
	if raw := c.Query("from"); strings.TrimSpace(raw) != "" {
		parsed, errParse := parseUsageTime(raw)
		if errParse != nil {
			return q, errParse
		}
		from = parsed
	} else if defaultWindow > 0 {
		from = to.Add(-defaultWindow)
	}
	if !from.Before(to) {
		return q, errors.New("from must be before to")
	}
	q.filter = usagestore.Filter{
		From:           from,
		To:             to,
		RequestedModel: c.Query("model"),
		UpstreamModel:  c.Query("upstream_model"),
		Provider:       c.Query("provider"),
		Credential:     c.Query("credential"),
		APIKey:         c.Query("api_key"),
	}
	if raw := strings.TrimSpace(c.Query("success")); raw != "" {
		success, errBool := strconv.ParseBool(raw)
		if errBool != nil {
			return q, fmt.Errorf("invalid success %q", raw)
		}
		q.filter.Success = &success
	}
	return q, nil
}

func usageWindowFields(q usageQuery) gin.H {
	zone := q.loc.String()
	if q.loc == time.Local {
		// Name the server zone when the container sets TZ; otherwise "Local".
		if tz := strings.TrimPrefix(strings.TrimSpace(os.Getenv("TZ")), ":"); tz != "" {
			zone = tz
		}
	}
	return gin.H{
		"from":     q.filter.From.In(q.loc).Format(usagestore.TimeLayout),
		"to":       q.filter.To.In(q.loc).Format(usagestore.TimeLayout),
		"timezone": zone,
	}
}

func intQuery(c *gin.Context, name string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return def, nil
	}
	v, errInt := strconv.Atoi(raw)
	if errInt != nil || v < min || v > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return v, nil
}

// GetUsageSummary returns totals for a window, optionally grouped.
func (h *Handler) GetUsageSummary(c *gin.Context) {
	q, errQuery := parseUsageQuery(c, defaultUsageWindow)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	groupBy := strings.TrimSpace(c.Query("group_by"))
	if _, isDim := usagestore.Dimensions[groupBy]; groupBy != "" && !isDim && !usagestore.TimeBuckets[groupBy] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_by must be one of model, upstream_model, provider, credential, api_key, day, hour"})
		return
	}
	store, ok := h.usageStore(c)
	if !ok {
		return
	}
	summary, errSummary := store.SummaryQuery(c.Request.Context(), q.filter, groupBy, q.loc)
	if errSummary != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSummary.Error()})
		return
	}
	out := usageWindowFields(q)
	out["group_by"] = groupBy
	out["totals"] = summary.Totals
	out["latency_p50_ms"] = summary.LatencyP50Ms
	out["latency_p95_ms"] = summary.LatencyP95Ms
	if groupBy != "" {
		groups := summary.Groups
		if groups == nil {
			groups = []usagestore.Group{}
		}
		out["groups"] = groups
	}
	stats := store.Stats()
	out["store"] = gin.H{"dropped": stats.Dropped, "retention_days": store.RetentionDays()}
	c.JSON(http.StatusOK, out)
}

// GetUsageTimeseries returns zero-filled hour/day buckets for charts.
func (h *Handler) GetUsageTimeseries(c *gin.Context) {
	q, errQuery := parseUsageQuery(c, defaultUsageWindow)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	bucket := strings.TrimSpace(c.DefaultQuery("bucket", "hour"))
	if !usagestore.TimeBuckets[bucket] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bucket must be hour or day"})
		return
	}
	groupBy := strings.TrimSpace(c.Query("group_by"))
	if _, isDim := usagestore.Dimensions[groupBy]; groupBy != "" && !isDim {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_by must be one of model, upstream_model, provider, credential, api_key"})
		return
	}
	top, errTop := intQuery(c, "top", defaultUsageTop, 1, maxUsageTop)
	if errTop != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errTop.Error()})
		return
	}
	store, ok := h.usageStore(c)
	if !ok {
		return
	}
	series, errSeries := store.TimeseriesQuery(c.Request.Context(), q.filter, bucket, groupBy, top, q.loc)
	if errSeries != nil {
		status := http.StatusInternalServerError
		if errors.Is(errSeries, usagestore.ErrTooManyBuckets) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": errSeries.Error()})
		return
	}
	out := usageWindowFields(q)
	out["bucket"] = bucket
	out["group_by"] = groupBy
	out["buckets"] = series.Buckets
	out["series"] = series.Series
	c.JSON(http.StatusOK, out)
}

// GetUsageRequests lists stored request rows newest first with keyset pagination.
func (h *Handler) GetUsageRequests(c *gin.Context) {
	q, errQuery := parseUsageQuery(c, 0)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuery.Error()})
		return
	}
	limit, errLimit := intQuery(c, "limit", defaultRequestsLimit, 1, maxRequestsLimit)
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLimit.Error()})
		return
	}
	var before int64
	if raw := strings.TrimSpace(c.Query("before")); raw != "" {
		parsed, errBefore := strconv.ParseInt(raw, 10, 64)
		if errBefore != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before must be a positive request id"})
			return
		}
		before = parsed
	}
	store, ok := h.usageStore(c)
	if !ok {
		return
	}
	rows, next, errRows := store.RequestsQuery(c.Request.Context(), q.filter, before, limit)
	if errRows != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errRows.Error()})
		return
	}
	var nextBefore any
	if next > 0 {
		nextBefore = next
	}
	c.JSON(http.StatusOK, gin.H{"requests": rows, "next_before": nextBefore})
}
