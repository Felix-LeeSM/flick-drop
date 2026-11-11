package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func MigrateAPI(ctx context.Context, conn *sql.DB) error {
	statements := []string{
		// Bodies, filenames, and Model A wrapped identities are age files
		// (contracts/age-format-v1.md); size_bytes is the exact body length.
		// Model A rows carry the access KDF, proof hash, and wrapped identity;
		// Model B rows carry none of them.
		`create table if not exists secrets (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			storage_key text not null,
			access_kdf_params_json text,
			access_proof_hash text,
			wrapped_identity text,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
			failed_access_count integer not null default 0 check (failed_access_count >= 0),
			-- M3 large uploads land in S3 first; a secret starts as pending_upload
			-- and flips to active once /finalize confirms the object exists.
			state text not null default 'active' check (state in ('active', 'pending_upload')),
			expires_at datetime not null,
			consumed_at datetime,
			created_at datetime not null,
			updated_at datetime not null,
			reclaim_enqueued_at datetime,
			check ((access_kdf_params_json is null) = (access_proof_hash is null)
				and (access_proof_hash is null) = (wrapped_identity is null))
		)`,
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
		`create table if not exists secret_payloads (
			secret_id text primary key,
			ciphertext blob not null,
			created_at datetime not null,
			foreign key (secret_id) references secrets(id) on delete cascade
		)`,
		`create table if not exists object_reconciliation_cursor (
            id integer primary key check (id = 1),
            continuation_token text not null default '',
            generation integer not null default 0
        )`,
		`insert or ignore into object_reconciliation_cursor (id) values (1)`,
		`create table if not exists object_reconciliation_pending (
            object_key text primary key,
            job_id text not null unique
        )`,
		// Per-backend reconciliation state: the single-row legacy cursor cannot
		// track two buckets, and the legacy pending table keys on object_key
		// alone, which collides when both backends hold the same key. Legacy
		// tables stay in place; both backends are seeded so scans start from the
		// first page each.
		`create table if not exists object_reconciliation_cursor_v2 (
			backend text primary key check (backend in ('s3_object', 'r2_object')),
			continuation_token text not null default '',
			generation integer not null default 0
		)`,
		`insert or ignore into object_reconciliation_cursor_v2 (backend) values ('s3_object')`,
		`insert or ignore into object_reconciliation_cursor_v2 (backend) values ('r2_object')`,
		`create table if not exists object_reconciliation_pending_v2 (
			backend text not null,
			object_key text not null,
			job_id text not null unique,
			primary key (backend, object_key)
		)`,
		`create index if not exists idx_secrets_storage_key on secrets(storage_key)`,
		// No FK: early payload reclamation must preserve the sender outcome.
		`create table if not exists secret_management (
			secret_id text primary key,
			token_hash blob not null check (length(token_hash) = 32),
			expires_at datetime not null,
			outcome text check (outcome in ('opened', 'locked', 'cancelled', 'unavailable'))
		)`,
		`create index if not exists idx_secret_management_expires_at on secret_management(expires_at, secret_id)`,
		// An opened secret's rows are deleted in the open transaction, but its
		// bucket body may still be streaming to the recipient. This row alone
		// keeps object reconciliation away from that body until grace_until; it
		// holds no secret ID column, kind, or size (the managed key itself is
		// managed/secrets/{id}). grace_until uses the fixed-width
		// secrets.managementTimeFormat so the reaper's purge compares as text.
		`create table if not exists secret_open_grace (
			object_key text primary key,
			grace_until datetime not null
		)`,
		`create index if not exists idx_secret_open_grace_until on secret_open_grace(grace_until)`,
		// Daily aggregate usage counters (internal/usage): counts only, no IDs,
		// IPs, or content metadata. Secrets and requests rows are hard-deleted on
		// expiry, so these rows are the durable usage history; they are never
		// deleted and stay at one row per day per metric.
		`create table if not exists usage_counters (
			day text not null,
			metric text not null,
			count integer not null default 0 check (count >= 0),
			primary key (day, metric)
		)`,
		requestsTableSchema,
		`create index if not exists idx_requests_expires_at on requests(expires_at, id)`,
		`create table if not exists request_payloads (
			request_id text primary key,
			ciphertext blob not null,
			foreign key (request_id) references requests(id) on delete cascade
		)`,
		`create table if not exists outbox_events (
			id text primary key,
			subject text not null,
			payload_json text not null,
			state text not null default 'pending'
				check (state in ('pending', 'published', 'failed')),
			attempts integer not null default 0 check (attempts >= 0),
			next_attempt_at datetime not null,
			published_at datetime,
			last_error text,
			created_at datetime not null,
			updated_at datetime not null
		)`,
		`create index if not exists idx_outbox_events_state_next_attempt
			on outbox_events(state, next_attempt_at)`,
	}

	// The AES-GCM format is gone and age clients cannot open its secrets, so a
	// table in that shape is dropped before the age table is created.
	if err := dropAESGCMSecrets(ctx, conn); err != nil {
		return err
	}
	if err := dropRSARequests(ctx, conn); err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply api migration: %w", err)
		}
	}
	// The dual-backend placement values are new; tables written before them
	// carry CHECK constraints that reject 'r2_object' and must be rebuilt in
	// place.
	if err := widenSecretsBackends(ctx, conn); err != nil {
		return err
	}
	// Covers both reaper conditions (active expiry + pending_upload orphan) in a
	// single partial index over the rows still awaiting reclaim.
	if _, err := conn.ExecContext(ctx, `create index if not exists idx_secrets_reclaim_pending
			on secrets(state, expires_at, created_at)
			where reclaim_enqueued_at is null and consumed_at is null`); err != nil {
		return fmt.Errorf("create reclaim-pending index: %w", err)
	}
	if err := forgetOpenedSecrets(ctx, conn, time.Now().UTC()); err != nil {
		return err
	}
	// Earlier releases never deleted published outbox rows, whose payloads name
	// object keys and secret IDs. This clears that backlog at once; afterwards
	// the API reaper prunes them in bounded batches (events.PurgePublishedTx).
	// The hour mirrors events.PublishedRetention; db cannot import events
	// because the events tests import db. Failed rows stay for diagnosis.
	if _, err := conn.ExecContext(ctx, `delete from outbox_events where state = 'published'
			and julianday(published_at) <= julianday(?)`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("purge published outbox events: %w", err)
	}
	return normalizeRequestsSchema(ctx, conn)
}

// forgetOpenedSecrets removes what earlier releases kept after a successful
// open: the consumed secrets row (with its payload, by cascade) and the
// 'opened' management outcome. An opened row is one with consumed_at set and
// view_count > 0; a lockout sets consumed_at without a view, so locked rows
// stay. An object-store row opened within objectGrace (storage_usage.go, the
// secrets openObjectGrace) may still be streaming, so its key and deadline move
// to secret_open_grace before the row goes. Runtime opens delete these rows
// themselves, so after the first run this is a no-op.
func forgetOpenedSecrets(ctx context.Context, conn *sql.DB, now time.Time) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin opened secrets purge: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	const opened = `consumed_at is not null and view_count > 0`
	graceMinutes := objectGrace.Minutes()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		// strftime's %f stops at milliseconds; the literal pads it to the
		// nanosecond width of secrets.managementTimeFormat.
		{`insert or ignore into secret_open_grace (object_key, grace_until)
			select storage_key, strftime('%Y-%m-%dT%H:%M:%f000000Z', julianday(consumed_at) + ? / 1440.0)
			from secrets where ` + opened + ` and storage_backend in ('s3_object', 'r2_object')
				and julianday(consumed_at) > julianday(?)`, []any{graceMinutes, now.Add(-objectGrace).Format(time.RFC3339Nano)}},
		{`delete from secret_management where outcome = 'opened'
			or secret_id in (select id from secrets where ` + opened + `)`, nil},
		{`delete from secrets where ` + opened, nil},
	} {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return fmt.Errorf("purge opened secrets: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit opened secrets purge: %w", err)
	}
	return nil
}

// dropAESGCMSecrets removes a secrets table that still has the AES-GCM `nonce`
// column, with its inline payloads. Senders' management pages report those
// secrets as unavailable; their bucket objects lose protection and object
// reconciliation deletes them. A no-op on a fresh or already-migrated database.
func dropAESGCMSecrets(ctx context.Context, conn *sql.DB) error {
	legacy, err := columnExists(ctx, conn, "secrets", "nonce")
	if err != nil || !legacy {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin aes-gcm secrets drop: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var hasManagement int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'secret_management'`).Scan(&hasManagement); err != nil {
		return fmt.Errorf("inspect secret_management: %w", err)
	}
	stmts := []string{`drop table if exists secret_payloads`, `drop table secrets`}
	if hasManagement == 1 {
		stmts = append([]string{`update secret_management set outcome = 'unavailable' where outcome is null`}, stmts...)
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop aes-gcm secrets: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit aes-gcm secrets drop: %w", err)
	}
	return nil
}

// widenSecretsBackends rebuilds the secrets table in place when its CHECK
// constraint predates the 'r2_object' placement value. SQLite cannot alter a
// CHECK, so the rebuild follows the documented 12-step table-rebuild procedure
// (create copy -> column-explicit insert -> drop -> rename) on a single
// connection with foreign keys off, because secret_payloads references this
// table and a plain drop would cascade-delete payloads.
func widenSecretsBackends(ctx context.Context, db *sql.DB) error {
	needsRebuild, err := tableSQLLacksValue(ctx, db, "secrets", "'r2_object'")
	if err != nil || !needsRebuild {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("widen secrets backends: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys = off`); err != nil {
		return fmt.Errorf("widen secrets backends: disable foreign keys: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `pragma foreign_keys = on`)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("widen secrets backends: begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, statement := range []string{
		`create table secrets_widen (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			storage_key text not null,
			access_kdf_params_json text,
			access_proof_hash text,
			wrapped_identity text,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
			failed_access_count integer not null default 0 check (failed_access_count >= 0),
			state text not null default 'active' check (state in ('active', 'pending_upload')),
			expires_at datetime not null,
			consumed_at datetime,
			created_at datetime not null,
			updated_at datetime not null,
			reclaim_enqueued_at datetime,
			check ((access_kdf_params_json is null) = (access_proof_hash is null)
				and (access_proof_hash is null) = (wrapped_identity is null))
		)`,
		`insert into secrets_widen (id, kind, storage_backend, storage_key, access_kdf_params_json,
			access_proof_hash, wrapped_identity, encrypted_filename, content_type, size_bytes,
			max_views, view_count, failed_access_count, state, expires_at, consumed_at, created_at,
			updated_at, reclaim_enqueued_at)
			select id, kind, storage_backend, storage_key, access_kdf_params_json,
			access_proof_hash, wrapped_identity, encrypted_filename, content_type, size_bytes,
			max_views, view_count, failed_access_count, state, expires_at, consumed_at, created_at,
			updated_at, reclaim_enqueued_at from secrets`,
		`drop table secrets`,
		`alter table secrets_widen rename to secrets`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("widen secrets backends: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("widen secrets backends: commit: %w", err)
	}
	for _, statement := range []string{
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
		`create index if not exists idx_secrets_storage_key on secrets(storage_key)`,
		`create index if not exists idx_secrets_reclaim_pending
			on secrets(state, expires_at, created_at)
			where reclaim_enqueued_at is null and consumed_at is null`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("widen secrets backends: restore indexes: %w", err)
		}
	}
	return nil
}

// tableSQLLacksValue reports whether table's CREATE statement in sqlite_master
// does not contain the given quoted SQL literal, i.e. the table predates the
// schema change that introduced it. A missing table is a no-op (the create
// statements in MigrateAPI build the widened shape).
func tableSQLLacksValue(ctx context.Context, db *sql.DB, table, literal string) (bool, error) {
	var sqlText string
	err := db.QueryRowContext(ctx, `select sql from sqlite_master where type = 'table' and name = ?`, table).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	return !strings.Contains(sqlText, literal), nil
}
func columnExists(ctx context.Context, conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s column info: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s column info: %w", table, err)
	}
	return false, nil
}

func MigrateWorker(ctx context.Context, conn *sql.DB) error {
	statements := []string{
		`create table if not exists job_receipts (
			job_id text primary key,
			kind text not null,
			state text not null
				check (state in ('processing', 'succeeded', 'failed', 'dead')),
			attempts integer not null default 0 check (attempts >= 0),
			last_error text,
			first_seen_at datetime not null,
			updated_at datetime not null,
			completed_at datetime
		)`,
		`create index if not exists idx_job_receipts_state_updated_at
			on job_receipts(state, updated_at)`,
		`create table if not exists job_attempts (
			id integer primary key autoincrement,
			job_id text not null,
			attempt integer not null,
			started_at datetime not null,
			finished_at datetime,
			result text not null check (result in ('running', 'succeeded', 'failed')),
			error text,
			foreign key (job_id) references job_receipts(job_id)
		)`,
		`create index if not exists idx_job_attempts_job_id on job_attempts(job_id)`,
		`create table if not exists dead_letters (
			job_id text primary key,
			kind text not null,
			payload_json text not null,
			error text not null,
			created_at datetime not null
		)`,
	}

	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply worker migration: %w", err)
		}
	}
	return nil
}
