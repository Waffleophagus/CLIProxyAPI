package usagestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pricing"
	log "github.com/sirupsen/logrus"
)

// dims is one rollup_keys row: every dimension queries filter, group or
// price by.
type dims struct {
	provider, authID, authLabel, authType, baseURL string
	requestedModel, upstreamModel, responseModel   string
	clientKeyID, clientKeyMasked, serviceTier      string
	band                                           int64
}

func dimsOf(r *Row) dims {
	return dims{
		provider: r.Provider, authID: r.AuthID, authLabel: r.AuthLabel, authType: r.AuthType, baseURL: r.BaseURL,
		requestedModel: r.RequestedModel, upstreamModel: r.UpstreamModel, responseModel: r.ResponseModel,
		clientKeyID: r.ClientKeyID, clientKeyMasked: r.ClientKeyMasked, serviceTier: r.ServiceTier,
		band: contextBand(r.InputTokens),
	}
}

func (d *dims) priceKey() pricing.Key {
	return pricing.Key{
		Provider: d.provider, AuthType: d.authType, BaseURL: d.baseURL, RequestedModel: d.requestedModel,
		UpstreamModel: d.upstreamModel, ResponseModel: d.responseModel, ServiceTier: d.serviceTier,
	}
}

// matches applies the filter's dimension fields (not the window or success),
// trimmed like whereRange so rollups and raw rows select the same rows.
func (f Filter) matches(d *dims) bool {
	eq := func(want, got string) bool {
		want = strings.TrimSpace(want)
		return want == "" || want == got
	}
	return eq(f.RequestedModel, d.requestedModel) &&
		eq(f.UpstreamModel, d.upstreamModel) &&
		eq(f.Provider, d.provider) &&
		eq(f.Credential, d.authID) &&
		eq(f.APIKey, d.clientKeyID)
}

// sums are additive request measures. Latency and TTFT only cover
// successful requests.
type sums struct {
	requests, input, output, reasoning, cacheRead, cacheCreation, total int64
	latencySum, latencyMax, ttftSum, ttftCount                          int64
}

func (s *sums) addRow(r *Row) {
	s.requests++
	s.input += r.InputTokens
	s.output += r.OutputTokens
	s.reasoning += r.ReasoningTokens
	s.cacheRead += r.CacheReadTokens
	s.cacheCreation += r.CacheCreationTokens
	s.total += r.TotalTokens
	if r.Success {
		s.latencySum += r.LatencyMs
		s.latencyMax = max(s.latencyMax, r.LatencyMs)
		if r.TTFTMs > 0 {
			s.ttftSum += r.TTFTMs
			s.ttftCount++
		}
	}
}

const upsertKeySQL = `INSERT INTO rollup_keys (provider, auth_id, auth_label, auth_type, base_url, requested_model,
	upstream_model, response_model, client_key_id, client_key_masked, service_tier, band)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, auth_id, auth_label, auth_type, base_url, requested_model, upstream_model, response_model,
	client_key_id, client_key_masked, service_tier, band) DO UPDATE SET band = excluded.band
RETURNING id`

const upsertRollupSQL = `INSERT INTO rollups (bucket, key_id, success, requests, input_tokens, output_tokens,
	reasoning_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, latency_sum, latency_max, ttft_sum, ttft_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (bucket, key_id, success) DO UPDATE SET
	requests = requests + excluded.requests,
	input_tokens = input_tokens + excluded.input_tokens,
	output_tokens = output_tokens + excluded.output_tokens,
	reasoning_tokens = reasoning_tokens + excluded.reasoning_tokens,
	cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
	cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens,
	total_tokens = total_tokens + excluded.total_tokens,
	latency_sum = latency_sum + excluded.latency_sum,
	latency_max = MAX(latency_max, excluded.latency_max),
	ttft_sum = ttft_sum + excluded.ttft_sum,
	ttft_count = ttft_count + excluded.ttft_count`

type rollupKey struct {
	bucket  int64
	dims    dims
	success bool
}

// upsertRollups folds a batch into the rollups inside the insert
// transaction, so rollups and raw rows always agree. It returns the key IDs
// it created or looked up, to be cached once the transaction commits.
func (s *Store) upsertRollups(ctx context.Context, tx *sql.Tx, rows []Row) (map[dims]int64, error) {
	batch := make(map[rollupKey]*sums)
	var order []rollupKey
	for i := range rows {
		r := &rows[i]
		k := rollupKey{bucket: (r.TS / sqlBucketMs) * sqlBucketMs, dims: dimsOf(r), success: r.Success}
		v := batch[k]
		if v == nil {
			v = &sums{}
			batch[k] = v
			order = append(order, k)
		}
		v.addRow(r)
	}
	s.keyMu.Lock()
	known := s.keyIDs
	s.keyMu.Unlock()
	fresh := make(map[dims]int64)
	keyStmt, errKey := tx.PrepareContext(ctx, upsertKeySQL)
	if errKey != nil {
		return nil, fmt.Errorf("prepare key: %w", errKey)
	}
	defer func() { _ = keyStmt.Close() }()
	stmt, errPrepare := tx.PrepareContext(ctx, upsertRollupSQL)
	if errPrepare != nil {
		return nil, fmt.Errorf("prepare: %w", errPrepare)
	}
	defer func() { _ = stmt.Close() }()
	for _, k := range order {
		id, ok := known[k.dims]
		if !ok {
			if id, ok = fresh[k.dims]; !ok {
				d := k.dims
				if errScan := keyStmt.QueryRowContext(ctx, d.provider, d.authID, d.authLabel, d.authType, d.baseURL,
					d.requestedModel, d.upstreamModel, d.responseModel, d.clientKeyID, d.clientKeyMasked,
					d.serviceTier, d.band).Scan(&id); errScan != nil {
					return nil, fmt.Errorf("key: %w", errScan)
				}
				fresh[k.dims] = id
			}
		}
		v := batch[k]
		if _, errExec := stmt.ExecContext(ctx, k.bucket, id, boolInt(k.success),
			v.requests, v.input, v.output, v.reasoning, v.cacheRead, v.cacheCreation, v.total,
			v.latencySum, v.latencyMax, v.ttftSum, v.ttftCount,
		); errExec != nil {
			return nil, fmt.Errorf("upsert: %w", errExec)
		}
	}
	return fresh, nil
}

// rememberKeys caches key IDs of a committed batch. The map is replaced, not
// mutated, so upsertRollups can read a snapshot without holding the lock.
func (s *Store) rememberKeys(fresh map[dims]int64) {
	if len(fresh) == 0 {
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	next := make(map[dims]int64, len(s.keyIDs)+len(fresh))
	for k, v := range s.keyIDs {
		next[k] = v
	}
	for k, v := range fresh {
		next[k] = v
	}
	s.keyIDs = next
}

// forgetKeys drops the key cache (after pruning deleted keys).
func (s *Store) forgetKeys() {
	s.keyMu.Lock()
	s.keyIDs = nil
	s.keyMu.Unlock()
}

// loadKeys reads every rollup key matching the filter's dimensions.
func loadKeys(ctx context.Context, tx querier, f Filter) (map[int64]*dims, error) {
	rows, errQuery := tx.QueryContext(ctx, `SELECT id, provider, auth_id, auth_label, auth_type, base_url,
	requested_model, upstream_model, response_model, client_key_id, client_key_masked, service_tier, band FROM rollup_keys`)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: rollup keys: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]*dims)
	for rows.Next() {
		var id int64
		d := &dims{}
		if errScan := rows.Scan(&id, &d.provider, &d.authID, &d.authLabel, &d.authType, &d.baseURL,
			&d.requestedModel, &d.upstreamModel, &d.responseModel, &d.clientKeyID, &d.clientKeyMasked,
			&d.serviceTier, &d.band); errScan != nil {
			return nil, fmt.Errorf("usagestore: rollup keys scan: %w", errScan)
		}
		if f.matches(d) {
			out[id] = d
		}
	}
	return out, rows.Err()
}

// aggregator folds rollup and raw rows into partials keyed by an optional
// 15-minute bucket, dimension value and price key, pricing every input row
// at the price in effect for its time.
type aggregator struct {
	f       Filter
	dim     *Dimension
	byTime  bool
	byPrice bool
	parts   map[partKey]*partial
}

type partKey struct {
	bucket int64
	key    string
	price  pricing.Key
}

func newAggregator(f Filter, dim *Dimension, byTime, byPrice bool) *aggregator {
	return &aggregator{f: f, dim: dim, byTime: byTime, byPrice: byPrice, parts: make(map[partKey]*partial)}
}

// add folds sums for d served at ms (bucket is ms's 15-minute bucket).
// res is d's price resolution, nil without a pricer.
func (a *aggregator) add(ms, bucket int64, d *dims, res *pricing.Resolution, success bool, v *sums) {
	var k partKey
	if a.byTime {
		k.bucket = bucket
	}
	label := ""
	if a.dim != nil {
		k.key, label = a.dim.key(d), a.dim.label(d)
	}
	if a.byPrice {
		k.price = d.priceKey()
		k.price.ServiceTier = ""
	}
	p := a.parts[k]
	if p == nil {
		p = &partial{bucket: k.bucket, key: k.key, label: label, price: k.price}
		a.parts[k] = p
	} else if label > p.label {
		p.label = label
	}
	t := &p.totals
	t.Requests += v.requests
	if success {
		t.Successes += v.requests
	} else {
		t.Failures += v.requests
	}
	t.InputTokens += v.input
	t.OutputTokens += v.output
	t.ReasoningTokens += v.reasoning
	t.CacheReadTokens += v.cacheRead
	t.CacheCreationTokens += v.cacheCreation
	t.TotalTokens += v.total
	t.latencySum += v.latencySum
	t.MaxLatencyMs = max(t.MaxLatencyMs, v.latencyMax)
	t.ttftSum += v.ttftSum
	t.ttftCount += v.ttftCount
	if res == nil {
		return
	}
	usd, priced := res.Cost(ms, bandInput(d.band), d.serviceTier, pricing.Tokens{
		Input: v.input, Output: v.output, Reasoning: v.reasoning, CacheRead: v.cacheRead, CacheWrite: v.cacheCreation,
	})
	if priced {
		t.addCost(usd, res.Class)
	} else {
		t.UnpricedRequests += v.requests
	}
}

func (a *aggregator) partials() []partial {
	out := make([]partial, 0, len(a.parts))
	for _, p := range a.parts {
		if p.label == "" {
			p.label = p.key
		}
		out = append(out, *p)
	}
	return out
}

// querier is a *sql.DB or the read transaction of one query.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanRollups folds whole buckets in [from, to); both must be bucket-aligned.
func scanRollups(ctx context.Context, tx querier, a *aggregator, from, to int64) error {
	keys, errKeys := loadKeys(ctx, tx, a.f)
	if errKeys != nil || len(keys) == 0 {
		return errKeys
	}
	query := `SELECT bucket, key_id, success, requests, input_tokens, output_tokens, reasoning_tokens,
	cache_read_tokens, cache_creation_tokens, total_tokens, latency_sum, latency_max, ttft_sum, ttft_count
FROM rollups WHERE bucket >= ? AND bucket < ?`
	args := []any{from, to}
	if a.f.Success != nil {
		query += " AND success = ?"
		args = append(args, boolInt(*a.f.Success))
	}
	rows, errQuery := tx.QueryContext(ctx, query, args...)
	if errQuery != nil {
		return fmt.Errorf("usagestore: rollups: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	resolutions := make(map[int64]*pricing.Resolution)
	for rows.Next() {
		var bucket, keyID int64
		var success int
		var v sums
		if errScan := rows.Scan(&bucket, &keyID, &success, &v.requests, &v.input, &v.output, &v.reasoning,
			&v.cacheRead, &v.cacheCreation, &v.total, &v.latencySum, &v.latencyMax, &v.ttftSum, &v.ttftCount); errScan != nil {
			return fmt.Errorf("usagestore: rollups scan: %w", errScan)
		}
		d := keys[keyID]
		if d == nil {
			continue
		}
		var res *pricing.Resolution
		if a.f.Pricer != nil {
			if res = resolutions[keyID]; res == nil {
				resolved := a.f.Pricer.Resolve(d.priceKey())
				res = &resolved
				resolutions[keyID] = res
			}
		}
		a.add(bucket, bucket, d, res, success == 1, &v)
	}
	return rows.Err()
}

// scanRaw folds raw requests in [from, to), for partial buckets at window edges.
func scanRaw(ctx context.Context, q querier, a *aggregator, from, to int64) error {
	where, args := a.f.whereRange("ts", from, to)
	rows, errQuery := q.QueryContext(ctx, `SELECT ts, provider, auth_id, auth_label, auth_type, base_url,
	requested_model, upstream_model, response_model, client_key_id, client_key_masked, service_tier, success,
	input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
	latency_ms, ttft_ms FROM requests WHERE `+where, args...)
	if errQuery != nil {
		return fmt.Errorf("usagestore: raw rows: %w", errQuery)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r Row
		var success int
		if errScan := rows.Scan(&r.TS, &r.Provider, &r.AuthID, &r.AuthLabel, &r.AuthType, &r.BaseURL,
			&r.RequestedModel, &r.UpstreamModel, &r.ResponseModel, &r.ClientKeyID, &r.ClientKeyMasked, &r.ServiceTier,
			&success, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CacheReadTokens, &r.CacheCreationTokens,
			&r.TotalTokens, &r.LatencyMs, &r.TTFTMs); errScan != nil {
			return fmt.Errorf("usagestore: raw rows scan: %w", errScan)
		}
		r.Success = success == 1
		d := dimsOf(&r)
		var v sums
		v.addRow(&r)
		var res *pricing.Resolution
		if a.f.Pricer != nil {
			resolved := a.f.Pricer.Resolve(d.priceKey())
			res = &resolved
		}
		a.add(r.TS, (r.TS/sqlBucketMs)*sqlBucketMs, &d, res, r.Success, &v)
	}
	return rows.Err()
}

// aggregate folds the window. Whole 15-minute buckets come from the rollups
// and partial buckets at either edge from the raw requests, so every window
// stays exact while long windows scale with rollups instead of requests.
// An edge below the raw pruning watermark may have lost raw rows, so its
// whole bucket is taken from the rollups instead (15-minute precision).
// Everything is read in one transaction, so the watermark, keys, rollups and
// raw rows all come from the same snapshot even while the writer prunes.
func (s *Store) aggregate(ctx context.Context, f Filter, dim *Dimension, byTime, byPrice bool) ([]partial, error) {
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return nil, fmt.Errorf("usagestore: aggregate: %w", errBegin)
	}
	defer func() { _ = tx.Rollback() }()
	a := newAggregator(f, dim, byTime, byPrice)
	from, to := f.From.UnixMilli(), f.To.UnixMilli()
	first, last := ceilBucket(from), (to/sqlBucketMs)*sqlBucketMs
	rawFrom, errRawFrom := rawCompleteFrom(ctx, tx)
	if errRawFrom != nil {
		return nil, errRawFrom
	}
	if from < rawFrom {
		first = (from / sqlBucketMs) * sqlBucketMs
	}
	if last < rawFrom {
		last = ceilBucket(to)
	}
	if first >= last {
		if errRaw := scanRaw(ctx, tx, a, from, to); errRaw != nil {
			return nil, errRaw
		}
		return a.partials(), nil
	}
	if errRollups := scanRollups(ctx, tx, a, first, last); errRollups != nil {
		return nil, errRollups
	}
	for _, edge := range [][2]int64{{from, first}, {last, to}} {
		if edge[0] < edge[1] {
			if errRaw := scanRaw(ctx, tx, a, edge[0], edge[1]); errRaw != nil {
				return nil, errRaw
			}
		}
	}
	return a.partials(), nil
}

// rawCompleteFrom returns the time (ms) from which every raw row is stored:
// the highest cutoff raw rows were ever pruned below, or the minimum when
// nothing was pruned.
func rawCompleteFrom(ctx context.Context, q querier) (int64, error) {
	var watermark int64
	errScan := q.QueryRowContext(ctx, "SELECT value FROM usage_meta WHERE key = ?", rawPrunedBeforeKey).Scan(&watermark)
	if errors.Is(errScan, sql.ErrNoRows) {
		return math.MinInt64, nil
	}
	if errScan != nil {
		return 0, fmt.Errorf("usagestore: raw watermark: %w", errScan)
	}
	return watermark, nil
}

// ceilBucket rounds ms up to a 15-minute boundary.
func ceilBucket(ms int64) int64 {
	b := (ms / sqlBucketMs) * sqlBucketMs
	if b < ms {
		b += sqlBucketMs
	}
	return b
}

// pruneOrphanKeys deletes keys no rollup references any more.
func (s *Store) pruneOrphanKeys() {
	if _, errDelete := s.db.Exec("DELETE FROM rollup_keys WHERE id NOT IN (SELECT DISTINCT key_id FROM rollups)"); errDelete != nil {
		log.Warnf("usage store: rollup key prune failed: %v", errDelete)
		return
	}
	s.forgetKeys()
}
