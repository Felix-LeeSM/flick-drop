package db

import (
	"context"
	"database/sql"
	"fmt"
)

// workerMigrateLockKey keys the session-level advisory lock that serializes
// MigrateWorker: two worker replicas (or a rolling restart overlap) booting at
// the same time would otherwise race concurrent DDL on the pg_type catalog.
// The value is an arbitrary constant; it only needs to be stable across
// releases and distinct from any other lock key in this module.
const workerMigrateLockKey = 0x464C49434B574B52 // "FLICKWKR"

// MigrateWorker creates the worker-owned tables in the flick_worker logical
// database. It runs on every worker start, so every statement is idempotent
// and the whole run holds one advisory lock for its duration.
func MigrateWorker(ctx context.Context, conn *sql.DB) error {
	pinned, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin postgres connection for worker migration: %w", err)
	}
	defer pinned.Close()

	// Session-level lock on the pinned connection: the pool would otherwise
	// hand the unlock statement to a different session than the lock holder.
	if _, err := pinned.ExecContext(ctx, `select pg_advisory_lock($1)`, workerMigrateLockKey); err != nil {
		return fmt.Errorf("acquire worker migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = pinned.ExecContext(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, workerMigrateLockKey)
	}()

	statements := []string{
		`create table if not exists job_receipts (
			job_id text primary key,
			kind text not null,
			state text not null
				check (state in ('processing', 'succeeded', 'failed', 'dead')),
			attempts integer not null default 0 check (attempts >= 0),
			last_error text,
			first_seen_at timestamptz not null,
			updated_at timestamptz not null,
			completed_at timestamptz
		)`,
		`create index if not exists idx_job_receipts_state_updated_at
			on job_receipts(state, updated_at)`,
		`create table if not exists job_attempts (
			id bigint generated always as identity primary key,
			job_id text not null references job_receipts(job_id),
			attempt integer not null,
			started_at timestamptz not null,
			finished_at timestamptz,
			result text not null check (result in ('running', 'succeeded', 'failed')),
			error text
		)`,
		`create index if not exists idx_job_attempts_job_id on job_attempts(job_id)`,
		`create table if not exists dead_letters (
			job_id text primary key,
			kind text not null,
			payload_json text not null,
			error text not null,
			created_at timestamptz not null
		)`,
	}

	for _, statement := range statements {
		if _, err := pinned.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply worker migration: %w", err)
		}
	}
	return nil
}
