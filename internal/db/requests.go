package db

import (
	"context"
	"database/sql"
	"fmt"
)

const requestsTableSchema = `create table if not exists requests (
			id text primary key,
			public_key text not null,
			fingerprint text not null,
			submission_token_hash blob not null check (length(submission_token_hash) = 32),
			retrieval_token_hash blob not null check (length(retrieval_token_hash) = 32),
			state text not null default 'waiting' check (state in ('waiting', 'uploading', 'submitted', 'consumed', 'cancelled', 'unavailable')),
			generation integer not null default 1 check (generation between 1 and 16),
			attempt_token_hash blob check (length(attempt_token_hash) = 32),
			attempt_body_hash blob check (length(attempt_body_hash) = 32),
			kind text check (kind in ('text', 'file')),
			size_bytes integer check (size_bytes >= 0),
			envelope_json text,
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 blob check (length(ciphertext_sha256) = 32),
			reservation_expires_at text,
			consumed_at text,
			expires_at text not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend in ('s3_object', 'r2_object') and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend not in ('s3_object', 'r2_object') or (final_key is not null and ciphertext_sha256 is not null)),
			check (state != 'submitted' or (kind is not null and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null))
		)`

// dropRSARequests removes request tables from before request links moved to
// age. Their public keys are RSA and their bodies RSA-OAEP/AES-GCM, which age
// clients cannot open; the age table is the only one that has consumed_at.
// Objects of dropped rows become unreferenced, and request reconciliation
// deletes them.
func dropRSARequests(ctx context.Context, db *sql.DB) error {
	exists, err := tableExists(ctx, db, "requests")
	if err != nil || !exists {
		return err
	}
	hasConsumedAt, err := columnExists(ctx, db, "requests", "consumed_at")
	if err != nil || hasConsumedAt {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{"drop table if exists request_payloads", "drop table requests"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("drop RSA request tables: %w", err)
		}
	}
	return tx.Commit()
}

func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&n)
	return n == 1, err
}

func normalizeRequestsSchema(ctx context.Context, db *sql.DB) (err error) {
	if err := widenRequestsBackends(ctx, db); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, statement := range []string{
		`create index if not exists idx_requests_expires_at on requests(expires_at, id)`,
		`create index if not exists idx_requests_reservations on requests(reservation_expires_at, id) where state = 'uploading'`,
		`create unique index if not exists idx_requests_upload_key on requests(upload_key) where upload_key is not null`,
		`create unique index if not exists idx_requests_final_key on requests(final_key) where final_key is not null`,
		`create table if not exists request_reconciliation_cursor (id integer primary key check (id = 1), continuation_token text not null default '', generation integer not null default 0)`,
		`insert or ignore into request_reconciliation_cursor (id) values (1)`,
		`create table if not exists request_reconciliation_pending (object_key text primary key, job_id text not null unique)`,
		// Per-backend reconciliation state: the single-row legacy cursor cannot
		// track two buckets, and the legacy pending table keys on object_key
		// alone, which collides when both backends hold the same key. Legacy
		// tables stay in place; both backends are seeded so scans start from the
		// first page each.
		`create table if not exists request_reconciliation_cursor_v2 (
			backend text primary key check (backend in ('s3_object', 'r2_object')),
			continuation_token text not null default '',
			generation integer not null default 0
		)`,
		`insert or ignore into request_reconciliation_cursor_v2 (backend) values ('s3_object')`,
		`insert or ignore into request_reconciliation_cursor_v2 (backend) values ('r2_object')`,
		`create table if not exists request_reconciliation_pending_v2 (
			backend text not null,
			object_key text not null,
			job_id text not null unique,
			primary key (backend, object_key)
		)`,
	} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate request storage: %w", err)
		}
	}
	return nil
}

// widenRequestsBackends rebuilds the requests table in place when its CHECK
// constraints predate the 'r2_object' placement value (same procedure as
// widenSecretsBackends; request_payloads references this table, so foreign keys
// must be off for the rebuild).
func widenRequestsBackends(ctx context.Context, db *sql.DB) error {
	needsRebuild, err := tableSQLLacksValue(ctx, db, "requests", "'r2_object'")
	if err != nil || !needsRebuild {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("widen requests backends: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys = off`); err != nil {
		return fmt.Errorf("widen requests backends: disable foreign keys: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `pragma foreign_keys = on`)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("widen requests backends: begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, statement := range []string{
		`create table requests_widen (
			id text primary key,
			public_key text not null,
			fingerprint text not null,
			submission_token_hash blob not null check (length(submission_token_hash) = 32),
			retrieval_token_hash blob not null check (length(retrieval_token_hash) = 32),
			state text not null default 'waiting' check (state in ('waiting', 'uploading', 'submitted', 'consumed', 'cancelled', 'unavailable')),
			generation integer not null default 1 check (generation between 1 and 16),
			attempt_token_hash blob check (length(attempt_token_hash) = 32),
			attempt_body_hash blob check (length(attempt_body_hash) = 32),
			kind text check (kind in ('text', 'file')),
			size_bytes integer check (size_bytes >= 0),
			envelope_json text,
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 blob check (length(ciphertext_sha256) = 32),
			reservation_expires_at text,
			consumed_at text,
			expires_at text not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend in ('s3_object', 'r2_object') and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend not in ('s3_object', 'r2_object') or (final_key is not null and ciphertext_sha256 is not null)),
			check (state != 'submitted' or (kind is not null and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null))
		)`,
		`insert into requests_widen (id, public_key, fingerprint, submission_token_hash, retrieval_token_hash,
			state, generation, attempt_token_hash, attempt_body_hash, kind, size_bytes, envelope_json,
			storage_backend, upload_key, final_key, ciphertext_sha256, reservation_expires_at, consumed_at,
			expires_at)
			select id, public_key, fingerprint, submission_token_hash, retrieval_token_hash,
			state, generation, attempt_token_hash, attempt_body_hash, kind, size_bytes, envelope_json,
			storage_backend, upload_key, final_key, ciphertext_sha256, reservation_expires_at, consumed_at,
			expires_at from requests`,
		`drop table requests`,
		`alter table requests_widen rename to requests`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("widen requests backends: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("widen requests backends: commit: %w", err)
	}
	return nil
}
