package db

import (
	"context"
	"testing"
	"time"
)

func TestRequestSchemaMigrationConstraintsAndCascade(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if _, err := conn.ExecContext(ctx, `insert into requests(id,public_key,fingerprint,submission_token_hash,retrieval_token_hash,expires_at)
		values('request-test','synthetic-public-key','synthetic-fingerprint',$1,$2,$3)`,
		make([]byte, 32), make([]byte, 32), expires); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`update requests set generation=0`, nil},
		{`update requests set generation=17`, nil},
		{`update requests set submission_token_hash=$1`, []any{make([]byte, 31)}},
		{`update requests set retrieval_token_hash=$1`, []any{make([]byte, 33)}},
		{`update requests set state='submitted'`, nil},
		{`update requests set attempt_token_hash=$1`, []any{make([]byte, 32)}},
	} {
		if _, err := conn.ExecContext(ctx, statement.sql, statement.args...); err == nil {
			t.Fatalf("invalid row accepted: %s", statement.sql)
		}
	}
	if _, err := conn.ExecContext(ctx, `insert into request_payloads(request_id,ciphertext) values('request-test',$1)`,
		make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `select count(*) from request_payloads`).Scan(&count); err != nil || count != 1 {
		t.Fatal("migration erased payload")
	}
	if _, err := conn.ExecContext(ctx, `delete from requests`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from request_payloads`).Scan(&count); err != nil || count != 0 {
		t.Fatal("request delete did not cascade")
	}
	worker := openTestWorkerDatabase(t)
	if err := MigrateWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	var searchSchema string
	if err := worker.QueryRowContext(ctx, `select current_schema()`).Scan(&searchSchema); err != nil {
		t.Fatal(err)
	}
	var workerTables int
	if err := worker.QueryRowContext(ctx, `select count(*) from information_schema.tables
		where table_schema = $1 and table_name in ('requests', 'request_payloads')`,
		searchSchema).Scan(&workerTables); err != nil || workerTables != 0 {
		t.Fatalf("request schema entered worker DB: tables = %d, err = %v", workerTables, err)
	}
}

func TestMigrateAPICreatesOutboxEvents(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)

	_, err := conn.ExecContext(ctx, `insert into outbox_events (
		id, subject, payload_json, next_attempt_at, created_at, updated_at
	) values ($1, $2, $3, $4, $5, $6)`,
		"job_1",
		"flick.jobs",
		`{"job_id":"job_1","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}

	var state string
	if err := conn.QueryRowContext(ctx, `select state from outbox_events where id = $1`, "job_1").Scan(&state); err != nil {
		t.Fatalf("select outbox event: %v", err)
	}
	if state != "pending" {
		t.Fatalf("state = %q, want pending", state)
	}
}

func TestManagementMigrationPreservesExistingSecrets(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if _, err := conn.ExecContext(ctx, `insert into secrets (id,kind,storage_backend,storage_key,size_bytes,expires_at,created_at,updated_at)
		values ('legacy','text','sqlite_blob','legacy',1,$1,$2,$3)`, day, day, day); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `drop table secret_management`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := MigrateAPI(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := conn.QueryRowContext(ctx, `select count(*) from secrets where id = 'legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy row lost: %d %v", count, err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_management`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy token fabricated: %d %v", count, err)
	}
	if _, err := conn.ExecContext(ctx, `insert into secret_management values ('legacy',$1,$2,'locked')`,
		make([]byte, 32), day); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `delete from secrets where id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from secret_management where outcome = 'locked'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outcome lost on migration/payload reclamation: %d %v", count, err)
	}
}

// 두 API 레플리카가 동시에 부팅하면 MigrateAPI가 동시에 돈다. advisory lock이
// 직렬화하지 않으면 동시 DDL이 pg_type 카탈로그에서 충돌해 한쪽 부팅이 실패한다.
func TestMigrateAPIConcurrentBootsSerialize(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if _, err := conn.ExecContext(ctx, `drop table usage_counters, outbox_events, secret_open_grace`); err != nil {
		t.Fatal(err)
	}

	const replicas = 4
	errs := make(chan error, replicas)
	for range replicas {
		go func() { errs <- MigrateAPI(ctx, conn) }()
	}
	for range replicas {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent MigrateAPI: %v", err)
		}
	}

	var tables int
	if err := conn.QueryRowContext(ctx, `select count(*) from information_schema.tables
		where table_schema = current_schema() and table_name in ('usage_counters', 'outbox_events', 'secret_open_grace')`).Scan(&tables); err != nil || tables != 3 {
		t.Fatalf("recreated tables = %d, want 3 (%v)", tables, err)
	}
}

func TestMigrateAPIPreservesRequestReconciliationProgress(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	// The reconciler seeds a backend's cursor on its first pass.
	if _, err := conn.ExecContext(ctx, `insert into request_reconciliation_cursor_v2 (backend) values ('r2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `update request_reconciliation_cursor_v2 set continuation_token='page-three',generation=7 where backend='r2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `insert into request_reconciliation_pending_v2 values ('r2','managed/requests/key','req-job')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var cursor string
	var generation int
	if err := conn.QueryRowContext(ctx, `select continuation_token,generation from request_reconciliation_cursor_v2 where backend='r2'`).Scan(&cursor, &generation); err != nil || cursor != "page-three" || generation != 7 {
		t.Fatalf("migration reset request cursor: %s %d %v", cursor, generation, err)
	}
	var job string
	if err := conn.QueryRowContext(ctx, `select job_id from request_reconciliation_pending_v2 where backend='r2' and object_key='managed/requests/key'`).Scan(&job); err != nil || job != "req-job" {
		t.Fatalf("migration lost request pending claim: %s %v", job, err)
	}
}

func TestMigrateAPIPreservesReconciliationProgress(t *testing.T) {
	ctx := context.Background()
	conn := openTestAPIDatabase(t)
	if _, err := conn.ExecContext(ctx, `drop table object_reconciliation_cursor_v2`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `drop table object_reconciliation_pending_v2`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	// The reconciler seeds a backend's cursor on its first pass.
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_cursor_v2 (backend) values ('oci')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `update object_reconciliation_cursor_v2 set continuation_token='page-two',generation=4 where backend='oci'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_pending_v2 values ('oci','managed/secrets/key','job')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var cursor string
	var generation int
	if err := conn.QueryRowContext(ctx, `select continuation_token,generation from object_reconciliation_cursor_v2 where backend='oci'`).Scan(&cursor, &generation); err != nil || cursor != "page-two" || generation != 4 {
		t.Fatalf("migration reset cursor: %s %d %v", cursor, generation, err)
	}
	if err := conn.QueryRowContext(ctx, `select job_id from object_reconciliation_pending_v2 where backend='oci' and object_key='managed/secrets/key'`).Scan(&cursor); err != nil || cursor != "job" {
		t.Fatalf("migration lost pending claim: %s %v", cursor, err)
	}
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_pending_v2 values ('oci','managed/secrets/key','other')`); err == nil {
		t.Fatal("duplicate pending key accepted")
	}
	if _, err := conn.ExecContext(ctx, `insert into object_reconciliation_pending_v2 values ('oci','managed/secrets/other','job')`); err == nil {
		t.Fatal("duplicate job fence accepted")
	}
}
