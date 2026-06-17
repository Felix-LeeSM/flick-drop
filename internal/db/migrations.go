package db

import (
	"context"
	"database/sql"
	"fmt"
)

func MigrateAPI(ctx context.Context, conn *sql.DB) error {
	statements := []string{
		`create table if not exists secrets (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null check (storage_backend in ('sqlite_blob', 'oci_object')),
			storage_key text not null,
			nonce text not null,
			kdf_algorithm text not null,
			kdf_salt text not null,
			kdf_params_json text not null,
			access_kdf_params_json text,
			access_proof_hash text,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
			failed_access_count integer not null default 0 check (failed_access_count >= 0),
			expires_at datetime not null,
			consumed_at datetime,
			created_at datetime not null,
			updated_at datetime not null
		)`,
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
		`create table if not exists secret_payloads (
			secret_id text primary key,
			ciphertext blob not null,
			created_at datetime not null,
			foreign key (secret_id) references secrets(id) on delete cascade
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

	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply api migration: %w", err)
		}
	}
	if err := ensureColumn(ctx, conn, "secrets", "access_kdf_params_json", "text"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "secrets", "access_proof_hash", "text"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "secrets", "failed_access_count", "integer not null default 0 check (failed_access_count >= 0)"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, conn *sql.DB, table, column, definition string) error {
	rows, err := conn.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
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
			return fmt.Errorf("scan %s column info: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s column info: %w", table, err)
	}

	if _, err := conn.ExecContext(ctx, "alter table "+table+" add column "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, column, err)
	}
	return nil
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
