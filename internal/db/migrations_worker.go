package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Serializes concurrent replica DDL on pg_type; must stay stable across releases.
const workerMigrateLockKey = 0x464C49434B574B52 // "FLICKWKR"

func MigrateWorker(ctx context.Context, conn *sql.DB) error {
	pinned, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin postgres connection for worker migration: %w", err)
	}
	defer pinned.Close()

	// Pinned connection: the pool could hand the unlock to a different session.
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
