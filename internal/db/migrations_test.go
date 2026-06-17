package db

import (
	"context"
	"testing"
)

func TestMigrateAPICreatesOutboxEvents(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into outbox_events (
		id, subject, payload_json, next_attempt_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?)`,
		"job_1",
		"burnlink.jobs",
		`{"job_id":"job_1","kind":"expire_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
		"2026-06-17T00:00:00Z",
		"2026-06-17T00:00:00Z",
		"2026-06-17T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}

	var state string
	if err := conn.QueryRowContext(ctx, `select state from outbox_events where id = ?`, "job_1").Scan(&state); err != nil {
		t.Fatalf("select outbox event: %v", err)
	}
	if state != "pending" {
		t.Fatalf("state = %q, want pending", state)
	}
}

func TestMigrateWorkerCreatesJobReceipts(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	if err := MigrateWorker(ctx, conn); err != nil {
		t.Fatalf("migrate worker: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into job_receipts (
		job_id, kind, state, attempts, first_seen_at, updated_at
	) values (?, ?, ?, 0, ?, ?)`,
		"job_1",
		"expire_secret",
		"processing",
		"2026-06-17T00:00:00Z",
		"2026-06-17T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert job receipt: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into job_attempts (
		job_id, attempt, started_at, result
	) values (?, 1, ?, ?)`,
		"job_1",
		"2026-06-17T00:00:00Z",
		"running",
	)
	if err != nil {
		t.Fatalf("insert job attempt: %v", err)
	}

	_, err = conn.ExecContext(ctx, `insert into dead_letters (
		job_id, kind, payload_json, error, created_at
	) values (?, ?, ?, ?, ?)`,
		"job_1",
		"expire_secret",
		`{"job_id":"job_1","kind":"expire_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
		"failed permanently",
		"2026-06-17T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert dead letter: %v", err)
	}
}
