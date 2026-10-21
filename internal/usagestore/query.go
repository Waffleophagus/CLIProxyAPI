package usagestore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// TimeLayout is the RFC3339 layout (millisecond precision) used in responses.
const TimeLayout = "2006-01-02T15:04:05.000Z07:00"

// sqlBucketMs is the granularity rows are pre-aggregated at in SQL before Go
// regroups them into local hours or days. Every real time zone offset is a
// multiple of 15 minutes, so these buckets always nest inside local hours/days.
const sqlBucketMs = int64(15 * time.Minute / time.Millisecond)

// Filter narrows queries. Empty fields match everything.
type Filter struct {
	From           time.Time
	To             time.Time
	RequestedModel string
	UpstreamModel  string
	Provider       string
	Credential     string // auth_id
	APIKey         string // client_key_id fingerprint
	Success        *bool
}

func (f Filter) where() (string, []any) {
	clauses := []string{"ts >= ?", "ts < ?"}
	args := []any{f.From.UnixMilli(), f.To.UnixMilli()}
	add := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			clauses = append(clauses, column+" = ?")
			args = append(args, value)
		}
	}
	add("requested_model", f.RequestedModel)
	add("upstream_model", f.UpstreamModel)
	add("provider", f.Provider)
	add("auth_id", f.Credential)
	add("client_key_id", f.APIKey)
	if f.Success != nil {
		clauses = append(clauses, "success = ?")
		args = append(args, boolInt(*f.Success))
	}
	return strings.Join(clauses, " AND "), args
}

// Totals aggregates a set of rows. Latency averages cover successful rows only;
// TTFT averages cover successful rows that reported a TTFT.
type Totals struct {
	Requests            int64   `json:"requests"`
	Successes           int64   `json:"successes"`
	Failures            int64   `json:"failures"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	AvgLatencyMs        float64 `json:"avg_latency_ms"`
	MaxLatencyMs        int64   `json:"max_latency_ms"`
	AvgTTFTMs           float64 `json:"avg_ttft_ms"`

	latencySum int64
	ttftSum    int64
	ttftCount  int64
}

func (t *Totals) add(o *Totals) {
	t.Requests += o.Requests
	t.Successes += o.Successes
	t.Failures += o.Failures
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.ReasoningTokens += o.ReasoningTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.CacheCreationTokens += o.CacheCreationTokens
	t.TotalTokens += o.TotalTokens
	t.latencySum += o.latencySum
	t.ttftSum += o.ttftSum
	t.ttftCount += o.ttftCount
	if o.MaxLatencyMs > t.MaxLatencyMs {
		t.MaxLatencyMs = o.MaxLatencyMs
	}
}

func (t *Totals) finish() {
	t.AvgLatencyMs, t.AvgTTFTMs = 0, 0
	if t.Successes > 0 {
		t.AvgLatencyMs = round1(float64(t.latencySum) / float64(t.Successes))
	}
	if t.ttftCount > 0 {
		t.AvgTTFTMs = round1(float64(t.ttftSum) / float64(t.ttftCount))
	}
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// Dimension identifies a group-by column.
type Dimension struct {
	Name  string
	key   string
	label string
}

// Dimensions are the non-time group-by options.
var Dimensions = map[string]Dimension{
	"model":          {Name: "model", key: "requested_model", label: "requested_model"},
	"upstream_model": {Name: "upstream_model", key: "upstream_model", label: "upstream_model"},
	"provider":       {Name: "provider", key: "provider", label: "provider"},
	"credential":     {Name: "credential", key: "auth_id", label: "auth_label"},
	"api_key":        {Name: "api_key", key: "client_key_id", label: "client_key_masked"},
}

// TimeBuckets are the time group-by / bucket options.
var TimeBuckets = map[string]bool{"hour": true, "day": true}

type partial struct {
	bucket int64
	key    string
	label  string
	totals Totals
}

// aggregate groups rows by an optional dimension and optional 15-minute bucket.
func (s *Store) aggregate(ctx context.Context, f Filter, dim *Dimension, byTime bool) ([]partial, error) {
	bucketExpr, keyExpr, labelExpr := "0", "''", "''"
	if byTime {
		bucketExpr = fmt.Sprintf("(ts / %d) * %d", sqlBucketMs, sqlBucketMs)
	}
	if dim != nil {
		keyExpr, labelExpr = dim.key, dim.label
	}
	where, args := f.where()
	query := fmt.Sprintf(`SELECT %s AS b, %s AS k, MAX(%s),
	COUNT(*), SUM(success),
	SUM(input_tokens), SUM(output_tokens), SUM(reasoning_tokens),
	SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(total_tokens),
	SUM(CASE WHEN success = 1 THEN latency_ms ELSE 0 END),
	MAX(CASE WHEN success = 1 THEN latency_ms ELSE 0 END),
	SUM(CASE WHEN success = 1 AND ttft_ms > 0 THEN ttft_ms ELSE 0 END),
	SUM(CASE WHEN success = 1 AND ttft_ms > 0 THEN 1 ELSE 0 END)
FROM requests WHERE %s GROUP BY b, k`, bucketExpr, keyExpr, labelExpr, where)
	rows, errQuery := s.db.QueryContext(ctx, query, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: aggregate: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	var out []partial
	for rows.Next() {
		var p partial
		t := &p.totals
		if errScan := rows.Scan(&p.bucket, &p.key, &p.label, &t.Requests, &t.Successes,
			&t.InputTokens, &t.OutputTokens, &t.ReasoningTokens,
			&t.CacheReadTokens, &t.CacheCreationTokens, &t.TotalTokens,
			&t.latencySum, &t.MaxLatencyMs, &t.ttftSum, &t.ttftCount); errScan != nil {
			return nil, fmt.Errorf("usagestore: aggregate scan: %w", errScan)
		}
		t.Failures = t.Requests - t.Successes
		if p.label == "" {
			p.label = p.key
		}
		out = append(out, p)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestore: aggregate rows: %w", errRows)
	}
	return out, nil
}

// bucketStart returns the instant at which the local hour or day containing t
// begins. Hours are truncated in absolute time using the offset in effect at
// t, so the two occurrences of a repeated (DST fall-back) hour stay distinct.
func bucketStart(t time.Time, bucket string, loc *time.Location) time.Time {
	t = t.In(loc)
	if bucket == "day" {
		return dayStart(t)
	}
	return t.Add(-(time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())))
}

// dayStart returns the first instant of t's local calendar day. When local
// midnight does not exist (DST starting at 00:00), the day begins at the
// zone transition.
func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	if sy, sm, sd := start.Date(); sy == y && sm == m && sd == d {
		return start
	}
	// time.Date resolved the missing midnight into the previous day; the day
	// starts where that zone period ends.
	if _, end := start.ZoneBounds(); !end.IsZero() && !end.After(t) {
		return end.In(t.Location())
	}
	return t
}

// nextBucket returns the start of the bucket after the one starting at t.
func nextBucket(t time.Time, bucket string, loc *time.Location) time.Time {
	if bucket == "day" {
		// Noon of the next calendar day always exists and is unambiguous.
		lt := t.In(loc)
		y, m, d := lt.Date()
		return dayStart(time.Date(y, m, d+1, 12, 0, 0, 0, loc))
	}
	return bucketStart(t.Add(time.Hour), bucket, loc)
}

// Group is one group-by row of a summary.
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Totals
}

// Summary is the response of SummaryQuery.
type Summary struct {
	Totals       Totals  `json:"totals"`
	LatencyP50Ms int64   `json:"latency_p50_ms"`
	LatencyP95Ms int64   `json:"latency_p95_ms"`
	Groups       []Group `json:"groups,omitempty"`
}

// SummaryQuery returns totals for the window, optionally grouped by a
// dimension or a local hour/day.
func (s *Store) SummaryQuery(ctx context.Context, f Filter, groupBy string, loc *time.Location) (*Summary, error) {
	var dim *Dimension
	byTime := TimeBuckets[groupBy]
	if d, ok := Dimensions[groupBy]; ok {
		dim = &d
	}
	parts, errAggregate := s.aggregate(ctx, f, dim, byTime)
	if errAggregate != nil {
		return nil, errAggregate
	}
	out := &Summary{}
	groups := make(map[string]*Group)
	order := make(map[string]int64)
	for i := range parts {
		p := &parts[i]
		out.Totals.add(&p.totals)
		if dim == nil && !byTime {
			continue
		}
		key, label := p.key, p.label
		if byTime {
			start := bucketStart(time.UnixMilli(p.bucket), groupBy, loc)
			key = start.Format(TimeLayout)
			label = key
			order[key] = start.UnixMilli()
		}
		g := groups[key]
		if g == nil {
			g = &Group{Key: key, Label: label}
			groups[key] = g
		}
		g.Totals.add(&p.totals)
	}
	out.Totals.finish()
	for _, g := range groups {
		g.Totals.finish()
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if byTime {
			return order[a.Key] < order[b.Key]
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		return a.Key < b.Key
	})
	percentiles, errPercentile := s.latencyPercentiles(ctx, f, 0.50, 0.95)
	if errPercentile != nil {
		return nil, errPercentile
	}
	out.LatencyP50Ms, out.LatencyP95Ms = percentiles[0], percentiles[1]
	return out, nil
}

// nearestRankIndex returns the zero-based index of the nearest-rank p-th
// percentile in n sorted values: ceil(p*n)-1, clamped to [0, n-1].
func nearestRankIndex(p float64, n int64) int64 {
	i := int64(math.Ceil(p*float64(n))) - 1
	return max(0, min(i, n-1))
}

// latencyPercentiles returns the nearest-rank percentiles ps (which must be
// ascending) of successful latencies. It reads one latency histogram, so the
// window is scanned and sorted once regardless of how many percentiles are
// requested, and the ranks come from the same snapshot as the values.
func (s *Store) latencyPercentiles(ctx context.Context, f Filter, ps ...float64) ([]int64, error) {
	out := make([]int64, len(ps))
	if f.Success != nil && !*f.Success {
		return out, nil
	}
	where, args := f.where()
	rows, errQuery := s.db.QueryContext(ctx, "SELECT latency_ms, COUNT(*) FROM requests WHERE "+where+" AND success = 1 GROUP BY latency_ms ORDER BY latency_ms", args...)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: latency percentiles: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	type bin struct{ latency, count int64 }
	var bins []bin
	var n int64
	for rows.Next() {
		var b bin
		if errScan := rows.Scan(&b.latency, &b.count); errScan != nil {
			return nil, fmt.Errorf("usagestore: latency percentiles scan: %w", errScan)
		}
		bins = append(bins, b)
		n += b.count
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestore: latency percentiles rows: %w", errRows)
	}
	if n == 0 {
		return out, nil
	}
	var seen int64 // values with a smaller latency
	next := 0
	for _, b := range bins {
		seen += b.count
		for next < len(ps) && nearestRankIndex(ps[next], n) < seen {
			out[next] = b.latency
			next++
		}
	}
	return out, nil
}

// Point is one time bucket of a series.
type Point struct {
	T string `json:"t"`
	Totals
}

// Series is one line of a timeseries chart.
type Series struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Totals Totals  `json:"totals"`
	Points []Point `json:"points"`
}

// Timeseries is the response of TimeseriesQuery.
type Timeseries struct {
	Buckets []string `json:"buckets"`
	Series  []Series `json:"series"`
}

// OtherSeriesKey identifies the series that folds groups beyond the top N.
const OtherSeriesKey = "__other__"

// MaxBuckets bounds the number of timeseries buckets per response.
const MaxBuckets = 5000

// ErrTooManyBuckets reports a timeseries window that spans more than MaxBuckets.
var ErrTooManyBuckets = errors.New("usagestore: window spans too many buckets")

// TimeseriesQuery returns zero-filled local hour/day buckets. With a dimension,
// the top N groups by request count get their own series and the rest are
// folded into an "other" series; without one there is a single "total" series.
func (s *Store) TimeseriesQuery(ctx context.Context, f Filter, bucket, groupBy string, top int, loc *time.Location) (*Timeseries, error) {
	if !TimeBuckets[bucket] {
		return nil, fmt.Errorf("usagestore: unsupported bucket %q", bucket)
	}
	var dim *Dimension
	if d, ok := Dimensions[groupBy]; ok {
		dim = &d
	}
	var starts []time.Time
	index := make(map[int64]int)
	for t := bucketStart(f.From, bucket, loc); t.Before(f.To); {
		if len(starts) >= MaxBuckets {
			return nil, fmt.Errorf("%w (max %d %s buckets)", ErrTooManyBuckets, MaxBuckets, bucket)
		}
		index[t.UnixMilli()] = len(starts)
		starts = append(starts, t)
		next := nextBucket(t, bucket, loc)
		if !next.After(t) {
			return nil, fmt.Errorf("usagestore: %s bucket after %s does not advance in %s", bucket, t.Format(TimeLayout), loc)
		}
		t = next
	}
	parts, errAggregate := s.aggregate(ctx, f, dim, true)
	if errAggregate != nil {
		return nil, errAggregate
	}

	// Rank groups over the whole window.
	type rank struct {
		key, label string
		totals     Totals
	}
	ranks := make(map[string]*rank)
	for i := range parts {
		r := ranks[parts[i].key]
		if r == nil {
			r = &rank{key: parts[i].key, label: parts[i].label}
			ranks[parts[i].key] = r
		}
		r.totals.add(&parts[i].totals)
	}
	ordered := make([]*rank, 0, len(ranks))
	for _, r := range ranks {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.totals.Requests != b.totals.Requests {
			return a.totals.Requests > b.totals.Requests
		}
		if a.totals.TotalTokens != b.totals.TotalTokens {
			return a.totals.TotalTokens > b.totals.TotalTokens
		}
		return a.key < b.key
	})

	seriesIndex := make(map[string]int)
	out := &Timeseries{Buckets: make([]string, len(starts))}
	for i, t := range starts {
		out.Buckets[i] = t.Format(TimeLayout)
	}
	newSeries := func(key, label string) int {
		points := make([]Point, len(starts))
		for i := range points {
			points[i].T = out.Buckets[i]
		}
		out.Series = append(out.Series, Series{Key: key, Label: label, Points: points})
		return len(out.Series) - 1
	}
	if dim == nil {
		newSeries("total", "total")
	} else {
		for i, r := range ordered {
			if i >= top {
				break
			}
			seriesIndex[r.key] = newSeries(r.key, r.label)
		}
		if len(ordered) > top {
			seriesIndex[OtherSeriesKey] = newSeries(OtherSeriesKey, "other")
		}
	}
	for i := range parts {
		p := &parts[i]
		pos, ok := index[bucketStart(time.UnixMilli(p.bucket), bucket, loc).UnixMilli()]
		if !ok {
			continue
		}
		si := 0
		if dim != nil {
			var found bool
			if si, found = seriesIndex[p.key]; !found {
				si = seriesIndex[OtherSeriesKey]
			}
		}
		out.Series[si].Points[pos].add(&p.totals)
		out.Series[si].Totals.add(&p.totals)
	}
	for si := range out.Series {
		out.Series[si].Totals.finish()
		for pi := range out.Series[si].Points {
			out.Series[si].Points[pi].finish()
		}
	}
	if out.Series == nil {
		out.Series = []Series{}
	}
	return out, nil
}

// RequestRow is one stored upstream attempt as returned by RequestsQuery.
type RequestRow struct {
	ID                  int64  `json:"id"`
	Time                string `json:"ts"`
	RequestID           string `json:"request_id"`
	TraceID             string `json:"trace_id,omitempty"`
	Provider            string `json:"provider"`
	AuthID              string `json:"auth_id"`
	AuthLabel           string `json:"auth_label"`
	AuthType            string `json:"auth_type,omitempty"`
	RequestedModel      string `json:"requested_model"`
	UpstreamModel       string `json:"upstream_model"`
	ResponseModel       string `json:"response_model,omitempty"`
	BaseURL             string `json:"base_url,omitempty"`
	ClientKeyID         string `json:"client_key_id"`
	ClientKeyMasked     string `json:"client_key_masked"`
	Endpoint            string `json:"endpoint,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	Stream              bool   `json:"stream"`
	Success             bool   `json:"success"`
	Status              int    `json:"status"`
	Error               string `json:"error,omitempty"`
	LatencyMs           int64  `json:"latency_ms"`
	TTFTMs              int64  `json:"ttft_ms"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
}

func requestsSQL(f Filter, before int64, limit int) (string, []any) {
	where, args := f.where()
	if before > 0 {
		where += " AND id < ?"
		args = append(args, before)
	}
	args = append(args, limit+1)
	return `SELECT id, ts, request_id, trace_id, provider, auth_id, auth_label, auth_type,
	requested_model, upstream_model, response_model, base_url, client_key_id, client_key_masked,
	endpoint, session_id, stream, success, status, error, latency_ms, ttft_ms,
	input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens,
	total_tokens, reasoning_effort, service_tier
FROM requests NOT INDEXED WHERE ` + where + ` ORDER BY id DESC LIMIT ?`, args
}

// RequestsQuery returns up to limit rows newest-first (by insertion id), with
// id < before when before > 0. The second value is the cursor for the next
// page, or 0 when there are no more rows.
//
// The query walks the primary key backwards (NOT INDEXED) and stops after
// limit+1 matches: an index plan on ts or a filter column would read and sort
// every matching row of the window just to return the newest page.
func (s *Store) RequestsQuery(ctx context.Context, f Filter, before int64, limit int) ([]RequestRow, int64, error) {
	query, args := requestsSQL(f, before, limit)
	rows, errQuery := s.db.QueryContext(ctx, query, args...)
	if errQuery != nil {
		return nil, 0, fmt.Errorf("usagestore: requests: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	out := make([]RequestRow, 0, limit)
	for rows.Next() {
		var r RequestRow
		var ts int64
		var stream, success int
		if errScan := rows.Scan(&r.ID, &ts, &r.RequestID, &r.TraceID, &r.Provider, &r.AuthID, &r.AuthLabel, &r.AuthType,
			&r.RequestedModel, &r.UpstreamModel, &r.ResponseModel, &r.BaseURL, &r.ClientKeyID, &r.ClientKeyMasked,
			&r.Endpoint, &r.SessionID, &stream, &success, &r.Status, &r.Error, &r.LatencyMs, &r.TTFTMs,
			&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CacheReadTokens, &r.CacheCreationTokens,
			&r.TotalTokens, &r.ReasoningEffort, &r.ServiceTier); errScan != nil {
			return nil, 0, fmt.Errorf("usagestore: requests scan: %w", errScan)
		}
		r.Time = time.UnixMilli(ts).UTC().Format(TimeLayout)
		r.Stream, r.Success = stream == 1, success == 1
		out = append(out, r)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, 0, fmt.Errorf("usagestore: requests rows: %w", errRows)
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}
