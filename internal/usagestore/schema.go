package usagestore

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations are applied in order; the database's PRAGMA user_version records
// how many have run. Append new steps, never edit shipped ones.
var migrations = []string{
	// 1: requests table (one row per upstream attempt).
	`CREATE TABLE requests (
	id                    INTEGER PRIMARY KEY AUTOINCREMENT,
	ts                    INTEGER NOT NULL,
	request_id            TEXT    NOT NULL DEFAULT '',
	trace_id              TEXT    NOT NULL DEFAULT '',
	provider              TEXT    NOT NULL DEFAULT '',
	auth_id               TEXT    NOT NULL DEFAULT '',
	auth_label            TEXT    NOT NULL DEFAULT '',
	auth_type             TEXT    NOT NULL DEFAULT '',
	requested_model       TEXT    NOT NULL DEFAULT '',
	upstream_model        TEXT    NOT NULL DEFAULT '',
	response_model        TEXT    NOT NULL DEFAULT '',
	base_url              TEXT    NOT NULL DEFAULT '',
	client_key_id         TEXT    NOT NULL DEFAULT '',
	client_key_masked     TEXT    NOT NULL DEFAULT '',
	endpoint              TEXT    NOT NULL DEFAULT '',
	session_id            TEXT    NOT NULL DEFAULT '',
	stream                INTEGER NOT NULL DEFAULT 0,
	success               INTEGER NOT NULL DEFAULT 1,
	status                INTEGER NOT NULL DEFAULT 0,
	error                 TEXT    NOT NULL DEFAULT '',
	latency_ms            INTEGER NOT NULL DEFAULT 0,
	ttft_ms               INTEGER NOT NULL DEFAULT 0,
	input_tokens          INTEGER NOT NULL DEFAULT 0,
	output_tokens         INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens      INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
	total_tokens          INTEGER NOT NULL DEFAULT 0,
	reasoning_effort      TEXT    NOT NULL DEFAULT '',
	service_tier          TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_requests_ts ON requests(ts);
CREATE INDEX idx_requests_requested_model_ts ON requests(requested_model, ts);
CREATE INDEX idx_requests_upstream_model_ts ON requests(upstream_model, ts);
CREATE INDEX idx_requests_provider_ts ON requests(provider, ts);
CREATE INDEX idx_requests_auth_id_ts ON requests(auth_id, ts);
CREATE INDEX idx_requests_client_key_id_ts ON requests(client_key_id, ts);`,
	// 2: rollups pre-aggregate requests per 15-minute bucket, so charts and
	// totals scale with buckets x combinations instead of requests and
	// outlive the raw rows. rollup_keys holds each distinct combination of
	// the dimensions queries filter, group or price by; band is the context
	// band lower bound (see contextBands). Latency and TTFT sums only cover
	// successful requests. Existing requests are backfilled.
	`CREATE TABLE rollup_keys (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	provider          TEXT    NOT NULL,
	auth_id           TEXT    NOT NULL,
	auth_label        TEXT    NOT NULL,
	auth_type         TEXT    NOT NULL,
	base_url          TEXT    NOT NULL,
	requested_model   TEXT    NOT NULL,
	upstream_model    TEXT    NOT NULL,
	response_model    TEXT    NOT NULL,
	client_key_id     TEXT    NOT NULL,
	client_key_masked TEXT    NOT NULL,
	service_tier      TEXT    NOT NULL,
	band              INTEGER NOT NULL,
	UNIQUE (provider, auth_id, auth_label, auth_type, base_url, requested_model, upstream_model, response_model,
		client_key_id, client_key_masked, service_tier, band)
);
CREATE TABLE rollups (
	bucket                INTEGER NOT NULL,
	key_id                INTEGER NOT NULL,
	success               INTEGER NOT NULL,
	requests              INTEGER NOT NULL,
	input_tokens          INTEGER NOT NULL,
	output_tokens         INTEGER NOT NULL,
	reasoning_tokens      INTEGER NOT NULL,
	cache_read_tokens     INTEGER NOT NULL,
	cache_creation_tokens INTEGER NOT NULL,
	total_tokens          INTEGER NOT NULL,
	latency_sum           INTEGER NOT NULL,
	latency_max           INTEGER NOT NULL,
	ttft_sum              INTEGER NOT NULL,
	ttft_count            INTEGER NOT NULL,
	PRIMARY KEY (bucket, key_id, success)
) WITHOUT ROWID;
CREATE TABLE usage_meta (
	key   TEXT    PRIMARY KEY,
	value INTEGER NOT NULL
) WITHOUT ROWID;
INSERT INTO rollup_keys (provider, auth_id, auth_label, auth_type, base_url, requested_model, upstream_model,
	response_model, client_key_id, client_key_masked, service_tier, band)
SELECT DISTINCT provider, auth_id, auth_label, auth_type, base_url, requested_model, upstream_model,
	response_model, client_key_id, client_key_masked, service_tier,
	CASE WHEN input_tokens > 512000 THEN 512000 WHEN input_tokens > 272000 THEN 272000
		WHEN input_tokens > 200000 THEN 200000 WHEN input_tokens > 100000 THEN 100000 ELSE 0 END
FROM requests;
INSERT INTO rollups
SELECT (r.ts / 900000) * 900000, k.id, r.success, COUNT(*), SUM(r.input_tokens), SUM(r.output_tokens),
	SUM(r.reasoning_tokens), SUM(r.cache_read_tokens), SUM(r.cache_creation_tokens), SUM(r.total_tokens),
	SUM(CASE WHEN r.success = 1 THEN r.latency_ms ELSE 0 END), MAX(CASE WHEN r.success = 1 THEN r.latency_ms ELSE 0 END),
	SUM(CASE WHEN r.success = 1 AND r.ttft_ms > 0 THEN r.ttft_ms ELSE 0 END),
	SUM(CASE WHEN r.success = 1 AND r.ttft_ms > 0 THEN 1 ELSE 0 END)
FROM requests r JOIN rollup_keys k ON k.provider = r.provider AND k.auth_id = r.auth_id
	AND k.auth_label = r.auth_label AND k.auth_type = r.auth_type AND k.base_url = r.base_url
	AND k.requested_model = r.requested_model AND k.upstream_model = r.upstream_model
	AND k.response_model = r.response_model AND k.client_key_id = r.client_key_id
	AND k.client_key_masked = r.client_key_masked AND k.service_tier = r.service_tier
	AND k.band = CASE WHEN r.input_tokens > 512000 THEN 512000 WHEN r.input_tokens > 272000 THEN 272000
		WHEN r.input_tokens > 200000 THEN 200000 WHEN r.input_tokens > 100000 THEN 100000 ELSE 0 END
GROUP BY 1, k.id, r.success;`,
	// 3: generation-speed sums over the requests that qualify for output
	// tokens per second (see genWindowMs). Rollups are backfilled from the
	// raw requests still stored; older buckets have no speed sample.
	`ALTER TABLE rollups ADD COLUMN gen_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rollups ADD COLUMN gen_ms INTEGER NOT NULL DEFAULT 0;
UPDATE rollups SET gen_tokens = g.tokens, gen_ms = g.ms
FROM (SELECT (r.ts / 900000) * 900000 AS bucket, k.id AS key_id, SUM(r.output_tokens) AS tokens,
		SUM(r.latency_ms - r.ttft_ms) AS ms
	FROM requests r JOIN rollup_keys k ON k.provider = r.provider AND k.auth_id = r.auth_id
		AND k.auth_label = r.auth_label AND k.auth_type = r.auth_type AND k.base_url = r.base_url
		AND k.requested_model = r.requested_model AND k.upstream_model = r.upstream_model
		AND k.response_model = r.response_model AND k.client_key_id = r.client_key_id
		AND k.client_key_masked = r.client_key_masked AND k.service_tier = r.service_tier
		AND k.band = CASE WHEN r.input_tokens > 512000 THEN 512000 WHEN r.input_tokens > 272000 THEN 272000
			WHEN r.input_tokens > 200000 THEN 200000 WHEN r.input_tokens > 100000 THEN 100000 ELSE 0 END
	WHERE r.success = 1 AND r.stream = 1 AND r.output_tokens > 0 AND r.ttft_ms > 0
		AND r.latency_ms - r.ttft_ms >= 250
	GROUP BY 1, 2) AS g
WHERE rollups.bucket = g.bucket AND rollups.key_id = g.key_id AND rollups.success = 1;`,
}

// minGenWindowMs is the shortest generation window (latency minus TTFT) a
// request needs to count towards output tokens per second. Some servers
// buffer a whole response (e.g. a parsed tool call) and send it at the end,
// so its first token arrives milliseconds before the last and the window
// says nothing about decode speed. Migration 3 hardcodes this value.
const minGenWindowMs = 250

// genWindowMs returns the generation window of a request that qualifies for
// output tokens per second, or 0. Like OpenCode's per-request TPS, the window
// runs from the first output token (reasoning included) to the end of the
// response. Only successful streams with a TTFT qualify: a non-streaming
// response arrives in one piece, so its TTFT is close to its latency.
func genWindowMs(r *Row) int64 {
	if !r.Success || !r.Stream || r.OutputTokens <= 0 || r.TTFTMs <= 0 {
		return 0
	}
	if w := r.LatencyMs - r.TTFTMs; w >= minGenWindowMs {
		return w
	}
	return 0
}

// contextBands are the input sizes (ascending) at which rows are split into
// separate rollups, so context-tiered prices (e.g. above 272k tokens) apply
// per band. They cover the first-party tiers (Anthropic 100k, Google and xAI
// 200k, OpenAI 272k, MiniMax 512k); other tier sizes are approximated by the
// enclosing band. Append only: stored bands must stay valid, and migration 2
// hardcodes this list.
var contextBands = []int64{100000, 200000, 272000, 512000}

// contextBand returns the band (lower bound) containing inputTokens.
func contextBand(inputTokens int64) int64 {
	var band int64
	for _, b := range contextBands {
		if inputTokens > b {
			band = b
		}
	}
	return band
}

// bandInput returns an input size that stands for the whole band when
// picking context-tiered prices: the band's upper edge, so a tier that
// starts anywhere inside the band applies to it.
func bandInput(band int64) int64 {
	for _, b := range contextBands {
		if b > band {
			return b
		}
	}
	return band + 1_000_000_000
}

// schemaVersion is the user_version of a fully migrated database.
var schemaVersion = len(migrations)

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if errVersion := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); errVersion != nil {
		return fmt.Errorf("read user_version: %w", errVersion)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	for i := version; i < schemaVersion; i++ {
		if errStep := applyMigration(ctx, db, i); errStep != nil {
			return fmt.Errorf("migration %d: %w", i+1, errStep)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, index int) error {
	// auto_vacuum must be set outside a transaction and before the first table
	// exists, so the first step sets it on the same connection that runs the DDL.
	// user_version is bumped in the same transaction as the step's DDL.
	conn, errConn := db.Conn(ctx)
	if errConn != nil {
		return errConn
	}
	defer func() { _ = conn.Close() }()
	if index == 0 {
		if _, errPragma := conn.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); errPragma != nil {
			return errPragma
		}
	}
	tx, errBegin := conn.BeginTx(ctx, nil)
	if errBegin != nil {
		return errBegin
	}
	if _, errExec := tx.ExecContext(ctx, migrations[index]); errExec != nil {
		_ = tx.Rollback()
		return errExec
	}
	if _, errVersion := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", index+1)); errVersion != nil {
		_ = tx.Rollback()
		return errVersion
	}
	return tx.Commit()
}
