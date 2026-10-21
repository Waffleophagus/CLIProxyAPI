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
