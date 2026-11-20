package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func legacyStorageBackendSchema(t *testing.T, ctx context.Context, exec func(string, ...any)) {
	t.Helper()
	for _, c := range namedStorageBackendChecks {
		exec(`alter table ` + c.table + ` drop constraint ` + c.name)
	}
	for _, statement := range []string{
		`alter table secrets add constraint secrets_storage_backend_check check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object'))`,
		`alter table requests add constraint requests_storage_backend_check check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object'))`,
		`alter table requests add constraint requests_check10 check (state != 'uploading' or storage_backend in ('s3_object', 'r2_object'))`,
		`alter table requests add constraint requests_check11 check (state != 'submitted' or storage_backend not in ('s3_object', 'r2_object') or final_key is not null)`,
		`alter table object_reconciliation_cursor_v2 add constraint object_reconciliation_cursor_v2_backend_check check (backend in ('s3_object', 'r2_object'))`,
		`alter table request_reconciliation_cursor_v2 add constraint request_reconciliation_cursor_v2_backend_check check (backend in ('s3_object', 'r2_object'))`,
		`insert into object_reconciliation_cursor_v2 (backend, generation) values ('s3_object', 7), ('r2_object', 3)`,
		`insert into request_reconciliation_cursor_v2 (backend) values ('s3_object'), ('r2_object')`,
		`insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ('r2_object', 'managed/secrets/b', 'job-pending')`,
		`insert into request_reconciliation_pending_v2 (backend, object_key, job_id) values ('s3_object', 'managed/requests/x/final', 'job-request-pending')`,
	} {
		exec(statement)
	}
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	exec(`insert into secrets (id, kind, storage_backend, storage_key, size_bytes, expires_at, created_at, updated_at)
		values ('a', 'file', 's3_object', 'managed/secrets/a', 10, $1, $1, $1),
			('b', 'file', 'r2_object', 'managed/secrets/b', 10, $1, $1, $1),
			('c', 'text', 'sqlite_blob', '', 10, $1, $1, $1)`, now)
	exec(`insert into requests (id, public_key, fingerprint, submission_token_hash, retrieval_token_hash, storage_backend, expires_at)
		values ('r', 'synthetic-public-key', 'synthetic-fingerprint', $1, $1, 'r2_object', $2)`, make([]byte, 32), now)
	exec(`insert into outbox_events (id, subject, payload_json, next_attempt_at, created_at, updated_at)
		values ('pending-delete', 'flick.jobs', '{"job_id":"j","kind":"delete_oci_object","object_key":"managed/secrets/a","backend":"s3_object"}', $1, $1, $1),
			('unnamed-delete', 'flick.jobs', '{"job_id":"u","kind":"delete_oci_object","object_key":"managed/secrets/u"}', $1, $1, $1),
			('pending-secret-delete', 'flick.jobs', '{"job_id":"s","kind":"delete_secret","secret_id":"gone"}', $1, $1, $1),
			('published-delete', 'flick.jobs', '{"job_id":"k","kind":"delete_oci_object","object_key":"managed/secrets/z","backend":"s3_object"}', $1, $1, $1)`, now)
	exec(`update outbox_events set state = 'published' where id = 'published-delete'`)
}

func TestLegacyStorageBackendNamesNeedTheRunbookBeforeMigrating(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	legacyStorageBackendSchema(t, ctx, exec)

	err := MigrateAPI(ctx, conn)

	if err == nil || !strings.Contains(err.Error(), legacyStorageBackendRunbook) {
		t.Fatalf("MigrateAPI with legacy rows = %v, want an error naming %s", err, legacyStorageBackendRunbook)
	}
	if got := scalar(`select count(*)::text from pg_constraint where conname = 'secrets_storage_backend_check'`); got != "1" {
		t.Fatalf("legacy secrets check count = %s, want it untouched after the refused migration", got)
	}

	runbook, err := os.ReadFile("../../docs/runbook/storage-backend-rename.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(runbook))
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("MigrateAPI after the runbook: %v", err)
	}

	if got := scalar(`select string_agg(id || '=' || storage_backend, ',' order by id) from secrets`); got != "a=oci,b=r2,c=sqlite_blob" {
		t.Fatalf("secrets backends = %s", got)
	}
	if got := scalar(`select storage_backend from requests where id = 'r'`); got != "r2" {
		t.Fatalf("request backend = %s, want r2", got)
	}
	if got := scalar(`select (select count(*) from object_reconciliation_pending_v2) + (select count(*) from request_reconciliation_pending_v2)`); got != "0" {
		t.Fatalf("legacy claims left = %s, want them dropped", got)
	}
	if got := scalar(`select string_agg(id, ',' order by id) from outbox_events`); got != "pending-delete,pending-secret-delete,published-delete" {
		t.Fatalf("outbox events = %s, want only the backend-less object delete dropped", got)
	}
	if got := scalar(`select payload_json from outbox_events where id = 'pending-delete'`); !strings.Contains(got, `"backend":"oci"`) {
		t.Fatalf("pending event payload = %s, want backend oci", got)
	}
	if got := scalar(`select payload_json from outbox_events where id = 'published-delete'`); !strings.Contains(got, `"backend":"s3_object"`) {
		t.Fatalf("published event payload = %s, want it left as history", got)
	}
	if got := scalar(`select count(*)::text from object_reconciliation_cursor_v2`); got != "0" {
		t.Fatalf("legacy cursor rows = %s, want them dropped for reseeding", got)
	}
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	insert := `insert into secrets (id, kind, storage_backend, storage_key, size_bytes, expires_at, created_at, updated_at) values ($1, 'file', $2, $3, 1, $4, $4, $4)`
	if _, err := conn.ExecContext(ctx, insert, "legacy", "s3_object", "managed/secrets/legacy", now); err == nil {
		t.Fatal("a new 's3_object' row was accepted after the migration")
	}
	if _, err := conn.ExecContext(ctx, insert, "named", "minio", "managed/secrets/named", now); err != nil {
		t.Fatalf("a configured backend name was rejected: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_cursor_v2 (backend) values ('r2_object')`); err == nil {
		t.Fatal("a legacy cursor name was accepted after the migration")
	}
}

func TestLegacyClaimsAloneMigrateWithoutTheRunbook(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// Claim tables carry no backend-name constraint.
	for _, statement := range []string{
		`insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ('s3_object', 'managed/secrets/b', 'job-pending')`,
		`insert into request_reconciliation_pending_v2 (backend, object_key, job_id) values ('r2_object', 'managed/requests/x/final', 'job-request-pending')`,
		`insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ('oci', 'managed/secrets/c', 'job-current')`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("MigrateAPI with only legacy claims: %v", err)
	}

	var left string
	if err := conn.QueryRowContext(ctx, `select string_agg(backend || ':' || object_key, ',') from (select backend, object_key from object_reconciliation_pending_v2 union all select backend, object_key from request_reconciliation_pending_v2) c`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != "oci:managed/secrets/c" {
		t.Fatalf("claims left = %s, want only the current-name claim", left)
	}
}

// A deployment that never used object storage.
func TestStorageBackendCheckMigrationIsANoOpWithoutObjectRows(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `alter table secrets drop constraint secrets_storage_backend_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `alter table secrets add constraint secrets_storage_backend_check check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_cursor_v2 (backend) values ('s3_object')`); err == nil {
		t.Fatal("fresh cursor table accepted a legacy name")
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("MigrateAPI on a deployment without object rows: %v", err)
	}
	var names string
	if err := conn.QueryRowContext(ctx, `select string_agg(conname, ',' order by conname) from pg_constraint where conrelid = 'secrets'::regclass and contype = 'c' and conname like 'secrets_storage_backend%'`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if names != "secrets_storage_backend_name" {
		t.Fatalf("secrets backend checks = %s, want only the name check", names)
	}
}
