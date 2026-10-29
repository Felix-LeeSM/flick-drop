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
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 blob check (length(ciphertext_sha256) = 32),
			reservation_expires_at text,
			consumed_at text,
			expires_at text not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend = 's3_object' and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend != 's3_object' or (final_key is not null and ciphertext_sha256 is not null)),
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
	} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate request storage: %w", err)
		}
	}
	return nil
}
