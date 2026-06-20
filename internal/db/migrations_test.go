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
		"flick.jobs",
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

func TestMigrateAPIRelaxesStrictKDFColumns(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	// Pre-Model-B schema: kdf columns are NOT NULL and the access columns do
	// not exist yet (added later by ensureColumn).
	if _, err := conn.ExecContext(ctx, `create table secrets (
		id text primary key,
		kind text not null,
		storage_backend text not null,
		storage_key text not null,
		nonce text not null,
		kdf_algorithm text not null,
		kdf_salt text not null,
		kdf_params_json text not null,
		encrypted_filename text,
		content_type text,
		size_bytes integer not null,
		max_views integer not null default 1,
		view_count integer not null default 0,
		failed_access_count integer not null default 0,
		expires_at datetime not null,
		consumed_at datetime,
		created_at datetime not null,
		updated_at datetime not null
	)`); err != nil {
		t.Fatalf("create strict secrets: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `create table secret_payloads (
		secret_id text primary key,
		ciphertext blob not null,
		created_at datetime not null,
		foreign key (secret_id) references secrets(id) on delete cascade
	)`); err != nil {
		t.Fatalf("create strict secret_payloads: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce,
		kdf_algorithm, kdf_salt, kdf_params_json,
		size_bytes, max_views, view_count, failed_access_count,
		expires_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 0, ?, ?, ?)`,
		"sec_1", "text", "sqlite_blob", "sec_1", "nonce",
		"PBKDF2-SHA-256", "salt", "{}",
		5,
		"2026-06-17T00:00:00Z", "2026-06-17T00:00:00Z", "2026-06-17T00:00:00Z",
	); err != nil {
		t.Fatalf("insert strict secret: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `insert into secret_payloads (secret_id, ciphertext, created_at) values (?, ?, ?)`,
		"sec_1", []byte("ciphertext"), "2026-06-17T00:00:00Z",
	); err != nil {
		t.Fatalf("insert strict payload: %v", err)
	}

	// Migrate: ensureColumn adds the access columns, then the table is rebuilt
	// to drop the NOT NULL kdf constraints.
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}

	// kdf columns are now nullable: a Model B row inserts cleanly.
	if _, err := conn.ExecContext(ctx, `insert into secrets (
		id, kind, storage_backend, storage_key, nonce,
		kdf_algorithm, kdf_salt, kdf_params_json,
		access_kdf_params_json, access_proof_hash,
		size_bytes, max_views, view_count, failed_access_count,
		expires_at, created_at, updated_at
	) values (?, ?, ?, ?, ?, null, null, null, null, null, ?, 1, 0, 0, ?, ?, ?)`,
		"sec_2", "text", "sqlite_blob", "sec_2", "nonce",
		5,
		"2026-06-17T00:00:00Z", "2026-06-17T00:00:00Z", "2026-06-17T00:00:00Z",
	); err != nil {
		t.Fatalf("insert model B secret after migration: %v", err)
	}

	// Existing Model A row and its payload survive the rebuild.
	var kind string
	if err := conn.QueryRowContext(ctx, `select kind from secrets where id = ?`, "sec_1").Scan(&kind); err != nil {
		t.Fatalf("select preserved secret: %v", err)
	}
	if kind != "text" {
		t.Fatalf("preserved kind = %q, want text", kind)
	}
	var payloadCount int
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_payloads where secret_id = ?`, "sec_1").Scan(&payloadCount); err != nil {
		t.Fatalf("count preserved payloads: %v", err)
	}
	if payloadCount != 1 {
		t.Fatalf("preserved payload count = %d, want 1", payloadCount)
	}

	// Idempotent: a second migration is a no-op (columns already nullable).
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("second migrate api: %v", err)
	}
}
