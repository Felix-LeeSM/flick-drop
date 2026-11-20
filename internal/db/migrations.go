package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Serializes concurrent replica DDL on pg_type; distinct from workerMigrateLockKey.
const apiMigrateLockKey = 0x464C49434B415049 // "FLICKAPI"

// Runs on every start: every statement is idempotent. No SQLite-era repair passes:
// Postgres arrives fresh or via the cutover runbook with the final schema.
func MigrateAPI(ctx context.Context, conn *sql.DB) error {
	pinned, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin postgres connection for api migration: %w", err)
	}
	defer pinned.Close()

	if _, err := pinned.ExecContext(ctx, `select pg_advisory_lock($1)`, apiMigrateLockKey); err != nil {
		return fmt.Errorf("acquire api migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = pinned.ExecContext(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, apiMigrateLockKey)
	}()

	statements := []string{
		// Bodies, filenames, and Model A wrapped identities are age files (contracts/age-format-v1.md).
		`create table if not exists secrets (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null constraint secrets_storage_backend_name check (storage_backend = 'sqlite_blob' or storage_backend ~ '^[a-z][a-z0-9]{0,15}$'),
			storage_key text not null,
			access_kdf_params_json text,
			access_proof_hash text,
			wrapped_identity text,
			encrypted_filename text,
			content_type text,
			size_bytes bigint not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
			failed_access_count integer not null default 0 check (failed_access_count >= 0),
			-- M3 large uploads land in S3 first; a secret starts as pending_upload
			-- and flips to active once /finalize confirms the object exists.
			state text not null default 'active' check (state in ('active', 'pending_upload')),
			expires_at timestamptz not null,
			consumed_at timestamptz,
			created_at timestamptz not null,
			updated_at timestamptz not null,
			reclaim_enqueued_at timestamptz,
			check ((access_kdf_params_json is null) = (access_proof_hash is null)
				and (access_proof_hash is null) = (wrapped_identity is null))
		)`,
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
		`create table if not exists secret_payloads (
			secret_id text primary key,
			ciphertext bytea not null,
			created_at timestamptz not null,
			foreign key (secret_id) references secrets(id) on delete cascade
		)`,
		`create table if not exists object_reconciliation_cursor_v2 (
			backend text primary key constraint object_reconciliation_cursor_v2_backend_name check (backend ~ '^[a-z][a-z0-9]{0,15}$'),
			continuation_token text not null default '',
			generation integer not null default 0
		)`,
		`create table if not exists object_reconciliation_pending_v2 (
			backend text not null,
			object_key text not null,
			job_id text not null unique,
			primary key (backend, object_key)
		)`,
		`create index if not exists idx_secrets_storage_key on secrets(storage_key)`,
		`create index if not exists idx_secrets_reclaim_pending
			on secrets(state, expires_at, created_at)
			where reclaim_enqueued_at is null and consumed_at is null`,
		// No FK: early payload reclamation must preserve the sender outcome.
		`create table if not exists secret_management (
			secret_id text primary key,
			token_hash bytea not null check (length(token_hash) = 32),
			expires_at timestamptz not null,
			outcome text check (outcome in ('opened', 'locked', 'cancelled', 'unavailable'))
		)`,
		`create index if not exists idx_secret_management_expires_at on secret_management(expires_at, secret_id)`,
		// Keeps reconciliation off a body still streaming after open until grace_until;
		// deliberately holds no secret ID, kind, or size.
		`create table if not exists secret_open_grace (
			object_key text primary key,
			grace_until timestamptz not null
		)`,
		`create index if not exists idx_secret_open_grace_until on secret_open_grace(grace_until)`,
		// Counts only, no IDs, IPs, or content metadata; never deleted.
		`create table if not exists usage_counters (
			day text not null,
			metric text not null,
			count bigint not null default 0 check (count >= 0),
			primary key (day, metric)
		)`,
		requestsTableSchema,
		`create table if not exists request_payloads (
			request_id text primary key,
			ciphertext bytea not null,
			foreign key (request_id) references requests(id) on delete cascade
		)`,
		`create table if not exists outbox_events (
			id text primary key,
			subject text not null,
			payload_json text not null,
			state text not null default 'pending'
				check (state in ('pending', 'published', 'failed')),
			attempts integer not null default 0 check (attempts >= 0),
			next_attempt_at timestamptz not null,
			published_at timestamptz,
			last_error text,
			created_at timestamptz not null,
			updated_at timestamptz not null
		)`,
		`create index if not exists idx_outbox_events_state_next_attempt
			on outbox_events(state, next_attempt_at)`,
	}

	for _, statement := range statements {
		if _, err := pinned.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply api migration: %w", err)
		}
	}
	if err := normalizeRequestsSchema(ctx, pinned); err != nil {
		return err
	}
	return retireLegacyStorageBackendChecks(ctx, pinned)
}
