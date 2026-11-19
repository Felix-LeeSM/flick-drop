package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// legacyStorageBackendSchema puts a freshly migrated database back into the
// shape the API had before FLICK_STORAGE_BACKENDS: inline enum checks under
// Postgres-generated names, seeded cursors, and rows on 's3_object'/'r2_object'.
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

// 고정 식별자가 남은 DB는 마이그레이션이 거부하고, 런북 SQL로 이름을 바꾼 뒤에야
// 이름 규칙 제약으로 넘어간다.
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
	// Given: 업그레이드 전 스키마와 's3_object'/'r2_object' 행
	legacyStorageBackendSchema(t, ctx, exec)

	// When: 이름을 바꾸기 전에 새 API가 마이그레이션한다
	err := MigrateAPI(ctx, conn)

	// Then: 런북을 가리키며 실패하고 옛 제약은 그대로다
	if err == nil || !strings.Contains(err.Error(), legacyStorageBackendRunbook) {
		t.Fatalf("MigrateAPI with legacy rows = %v, want an error naming %s", err, legacyStorageBackendRunbook)
	}
	if got := scalar(`select count(*)::text from pg_constraint where conname = 'secrets_storage_backend_check'`); got != "1" {
		t.Fatalf("legacy secrets check count = %s, want it untouched after the refused migration", got)
	}

	// When: 운영자가 런북 SQL을 돌린 뒤 다시 마이그레이션한다
	runbook, err := os.ReadFile("../../docs/runbook/storage-backend-rename.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(runbook))
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("MigrateAPI after the runbook: %v", err)
	}

	// Then: 행과 대기 이벤트가 새 이름을 쓰고, 발행된 이벤트와 인라인 행은 그대로다
	if got := scalar(`select string_agg(id || '=' || storage_backend, ',' order by id) from secrets`); got != "a=oci,b=r2,c=sqlite_blob" {
		t.Fatalf("secrets backends = %s", got)
	}
	if got := scalar(`select storage_backend from requests where id = 'r'`); got != "r2" {
		t.Fatalf("request backend = %s, want r2", got)
	}
	// Then: 옛 이름의 청구는 지워져 재조정이 새 이름으로 다시 청구할 수 있다
	if got := scalar(`select (select count(*) from object_reconciliation_pending_v2) + (select count(*) from request_reconciliation_pending_v2)`); got != "0" {
		t.Fatalf("legacy claims left = %s, want them dropped", got)
	}
	// Then: backend 없는 옛 객체 삭제는 지워지고 다른 대기 작업은 남는다
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
	// Then: 새 이름 규칙 제약이 적용돼 옛 식별자는 거부되고 설정 이름은 받아들여진다
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

// 옛 이름이 청구와 커서에만 남은 DB는 런북 없이 넘어가고 그 행들은 지워진다.
func TestLegacyClaimsAloneMigrateWithoutTheRunbook(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// Given: 행은 없고 옛 이름의 청구만 남았다(청구 테이블엔 이름 제약이 없다)
	for _, statement := range []string{
		`insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ('s3_object', 'managed/secrets/b', 'job-pending')`,
		`insert into request_reconciliation_pending_v2 (backend, object_key, job_id) values ('r2_object', 'managed/requests/x/final', 'job-request-pending')`,
		`insert into object_reconciliation_pending_v2 (backend, object_key, job_id) values ('oci', 'managed/secrets/c', 'job-current')`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	// When: 새 API가 마이그레이션한다
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("MigrateAPI with only legacy claims: %v", err)
	}

	// Then: 옛 이름의 청구만 지워진다
	var left string
	if err := conn.QueryRowContext(ctx, `select string_agg(backend || ':' || object_key, ',') from (select backend, object_key from object_reconciliation_pending_v2 union all select backend, object_key from request_reconciliation_pending_v2) c`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != "oci:managed/secrets/c" {
		t.Fatalf("claims left = %s, want only the current-name claim", left)
	}
}

// 고정 식별자가 없는 DB(객체 저장소를 안 쓰던 배포)는 런북 없이 바로 넘어간다.
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
		// The fresh cursor check forbids the legacy name, as intended; seeding it
		// needs the legacy check, which this case does not need.
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
