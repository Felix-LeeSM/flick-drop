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
			encrypted_note text,
			submission_token_hash bytea not null check (length(submission_token_hash) = 32),
			retrieval_token_hash bytea not null check (length(retrieval_token_hash) = 32),
			state text not null default 'waiting' check (state in ('waiting', 'uploading', 'submitted', 'consumed', 'cancelled', 'unavailable')),
			generation integer not null default 1 check (generation between 1 and 16),
			attempt_token_hash bytea check (length(attempt_token_hash) = 32),
			attempt_body_hash bytea check (length(attempt_body_hash) = 32),
			kind text check (kind in ('text', 'file')),
			size_bytes integer check (size_bytes >= 0),
			envelope_json text,
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 bytea check (length(ciphertext_sha256) = 32),
			reservation_expires_at timestamptz,
			consumed_at timestamptz,
			expires_at timestamptz not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend in ('s3_object', 'r2_object') and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend not in ('s3_object', 'r2_object') or (final_key is not null and ciphertext_sha256 is not null)),
			check (state != 'submitted' or (kind is not null and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null))
		)`

// normalizeRequestsSchema creates the request indexes and per-backend
// reconciliation tables. Every statement is idempotent; the schema itself is
// complete from requestsTableSchema, so there is nothing to detect or repair.
func normalizeRequestsSchema(ctx context.Context, conn *sql.Conn) (err error) {
	for _, statement := range []string{
		`create index if not exists idx_requests_expires_at on requests(expires_at, id)`,
		`create index if not exists idx_requests_reservations on requests(reservation_expires_at, id) where state = 'uploading'`,
		`create unique index if not exists idx_requests_upload_key on requests(upload_key) where upload_key is not null`,
		`create unique index if not exists idx_requests_final_key on requests(final_key) where final_key is not null`,
		`create table if not exists request_reconciliation_cursor_v2 (
			backend text primary key check (backend in ('s3_object', 'r2_object')),
			continuation_token text not null default '',
			generation integer not null default 0
		)`,
		`insert into request_reconciliation_cursor_v2 (backend) values ('s3_object') on conflict do nothing`,
		`insert into request_reconciliation_cursor_v2 (backend) values ('r2_object') on conflict do nothing`,
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
