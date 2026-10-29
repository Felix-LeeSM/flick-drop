package db

import (
	"context"
	"testing"
)

func TestRequestSchemaMigrationConstraintsAndCascade(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for range 2 {
		if err := MigrateAPI(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(`insert into requests(id,public_key,fingerprint,submission_token_hash,retrieval_token_hash,expires_at)
		values('request-test','synthetic-public-key','synthetic-fingerprint',zeroblob(32),zeroblob(32),'2026-10-08T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`update requests set generation=0`, `update requests set generation=17`,
		`update requests set submission_token_hash=zeroblob(31)`,
		`update requests set retrieval_token_hash=zeroblob(33)`,
		`update requests set state='submitted'`,
		`update requests set attempt_token_hash=zeroblob(32)`,
	} {
		if _, err := conn.Exec(statement); err == nil {
			t.Fatalf("invalid row accepted: %s", statement)
		}
	}
	if _, err := conn.Exec(`insert into request_payloads(request_id,ciphertext) values('request-test',zeroblob(16))`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(`select count(*) from request_payloads`).Scan(&count); err != nil || count != 1 {
		t.Fatal("migration erased payload")
	}
	if _, err := conn.Exec(`delete from requests`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select count(*) from request_payloads`).Scan(&count); err != nil || count != 0 {
		t.Fatal("request delete did not cascade")
	}
	worker, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := MigrateWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := worker.QueryRow(`select count(*) from sqlite_master where name in ('requests','request_payloads')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("request schema entered worker DB")
	}
}

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
		`{"job_id":"job_1","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
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
		"delete_secret",
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
		"delete_secret",
		`{"job_id":"job_1","kind":"delete_secret","secret_id":"sec_1","requested_at":"2026-06-17T00:00:00Z"}`,
		"failed permanently",
		"2026-06-17T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert dead letter: %v", err)
	}
}

// TestMigrateAPIDropsAESGCMSecrets pins the format cut-over: a secrets table
// that still has the AES-GCM nonce column is dropped with its payloads, the
// senders' pending management rows read as unavailable, and the age table takes
// its place. A second migration leaves the age table and its rows alone.
func TestMigrateAPIDropsAESGCMSecrets(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	// The AES-GCM shape as last shipped, with one inline secret, its payload,
	// and management rows for a pending and an already-opened secret.
	for _, statement := range []string{
		`create table secrets (
			id text primary key,
			kind text not null,
			storage_backend text not null,
			storage_key text not null,
			nonce text not null,
			kdf_algorithm text,
			kdf_salt text,
			kdf_params_json text,
			access_kdf_params_json text,
			access_proof_hash text,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null,
			max_views integer not null default 1,
			view_count integer not null default 0,
			failed_access_count integer not null default 0,
			state text not null default 'active',
			expires_at datetime not null,
			consumed_at datetime,
			created_at datetime not null,
			updated_at datetime not null,
			reclaim_enqueued_at datetime
		)`,
		`create table secret_payloads (
			secret_id text primary key,
			ciphertext blob not null,
			created_at datetime not null,
			foreign key (secret_id) references secrets(id) on delete cascade
		)`,
		`create table secret_management (
			secret_id text primary key,
			token_hash blob not null check (length(token_hash) = 32),
			expires_at datetime not null,
			outcome text check (outcome in ('opened', 'locked', 'cancelled', 'unavailable'))
		)`,
		`insert into secrets (id, kind, storage_backend, storage_key, nonce, size_bytes, expires_at, created_at, updated_at)
			values ('sec_1', 'text', 'sqlite_blob', 'sec_1', 'nonce', 5, '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z')`,
		`insert into secret_payloads values ('sec_1', X'0102', '2026-06-17T00:00:00Z')`,
		`insert into secret_management values ('sec_1', zeroblob(32), '2026-06-17T00:00:00Z', null)`,
		`insert into secret_management values ('sec_0', zeroblob(32), '2026-06-17T00:00:00Z', 'opened')`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed aes-gcm schema: %v", err)
		}
	}

	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("migrate api: %v", err)
	}

	hasNonce, err := columnExists(ctx, conn, "secrets", "nonce")
	if err != nil || hasNonce {
		t.Fatalf("nonce column after migration = %v, %v; want dropped", hasNonce, err)
	}
	hasWrapped, err := columnExists(ctx, conn, "secrets", "wrapped_identity")
	if err != nil || !hasWrapped {
		t.Fatalf("wrapped_identity column after migration = %v, %v; want present", hasWrapped, err)
	}
	var count int
	for _, table := range []string{"secrets", "secret_payloads"} {
		if err := conn.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows after migration = %d, %v; want 0", table, count, err)
		}
	}
	outcomes := map[string]string{}
	rows, err := conn.Query(`select secret_id, outcome from secret_management`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, outcome string
		if err := rows.Scan(&id, &outcome); err != nil {
			t.Fatal(err)
		}
		outcomes[id] = outcome
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if outcomes["sec_1"] != "unavailable" || outcomes["sec_0"] != "opened" || len(outcomes) != 2 {
		t.Fatalf("management outcomes = %v, want sec_1 unavailable and sec_0 opened", outcomes)
	}

	// The age table's CHECK: Model A columns are all present or all absent.
	if _, err := conn.Exec(`insert into secrets (id, kind, storage_backend, storage_key, access_kdf_params_json, access_proof_hash, size_bytes, expires_at, created_at, updated_at)
		values ('mixed', 'text', 'sqlite_blob', 'mixed', '{}', 'hash', 40, '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z')`); err == nil {
		t.Fatal("Model A row without wrapped_identity accepted")
	}
	if _, err := conn.Exec(`insert into secrets (id, kind, storage_backend, storage_key, access_kdf_params_json, access_proof_hash, wrapped_identity, size_bytes, expires_at, created_at, updated_at)
		values ('age_a', 'text', 'sqlite_blob', 'age_a', '{}', 'hash', 'identity', 40, '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z', '2026-06-17T00:00:00Z')`); err != nil {
		t.Fatalf("insert age Model A row: %v", err)
	}

	// Idempotent: the age table has no nonce column, so it is not dropped again.
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatalf("second migrate api: %v", err)
	}
	if err := conn.QueryRow(`select count(*) from secrets where id = 'age_a'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("age row after second migration = %d, %v; want kept", count, err)
	}
}

func TestManagementMigrationPreservesExistingSecrets(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into secrets (id,kind,storage_backend,storage_key,size_bytes,expires_at,created_at,updated_at)
		values ('legacy','text','sqlite_blob','legacy',1,'2026-10-08','2026-10-08','2026-10-08')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`drop table secret_management`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := MigrateAPI(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := conn.QueryRow(`select count(*) from secrets where id = 'legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy row lost: %d %v", count, err)
	}
	if err := conn.QueryRow(`select count(*) from secret_management`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy token fabricated: %d %v", count, err)
	}
	if _, err := conn.Exec(`insert into secret_management values ('legacy', zeroblob(32), '2026-10-08T00:00:00.000000000Z', 'opened')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`delete from secrets where id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select count(*) from secret_management where outcome = 'opened'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outcome lost on migration/payload reclamation: %d %v", count, err)
	}
}

func TestMigrateAPIPreservesReconciliationProgress(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`drop table object_reconciliation_cursor; drop table object_reconciliation_pending`)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`update object_reconciliation_cursor set continuation_token='page-two',generation=4; insert into object_reconciliation_pending values ('managed/secrets/key','job')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var cursor, job string
	var generation int
	if err := conn.QueryRow(`select continuation_token,generation from object_reconciliation_cursor`).Scan(&cursor, &generation); err != nil || cursor != "page-two" || generation != 4 {
		t.Fatalf("migration reset cursor: %s %d %v", cursor, generation, err)
	}
	if err := conn.QueryRow(`select job_id from object_reconciliation_pending`).Scan(&job); err != nil || job != "job" {
		t.Fatalf("migration lost pending claim: %s %v", job, err)
	}
	if _, err := conn.Exec(`insert into object_reconciliation_pending values ('managed/secrets/key','other')`); err == nil {
		t.Fatal("duplicate pending key accepted")
	}
	if _, err := conn.Exec(`insert into object_reconciliation_pending values ('managed/secrets/other','job')`); err == nil {
		t.Fatal("duplicate job fence accepted")
	}
}

// TestMigrateAPIDropsRSARequests pins the request format cut-over: a requests
// table without consumed_at holds RSA-OAEP/AES-GCM submissions no client can
// open any more, so it is dropped with its payloads and the age schema takes
// its place. A second migration leaves the age table and its rows alone.
func TestMigrateAPIDropsRSARequests(t *testing.T) {
	ctx := context.Background()
	conn, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// An RSA-era shape, including its restrictive state CHECK.
	for _, statement := range []string{
		`create table requests (id text primary key, public_key text not null, fingerprint text not null, submission_token_hash blob not null check(length(submission_token_hash)=32), retrieval_token_hash blob not null check(length(retrieval_token_hash)=32), state text not null default 'waiting' check(state in ('waiting','submitted','consumed','cancelled')), generation integer not null default 1 check(generation between 1 and 16), attempt_token_hash blob, attempt_body_hash blob, kind text, size_bytes integer, envelope_json text, expires_at text not null)`,
		`create table request_payloads(request_id text primary key, ciphertext blob not null, foreign key(request_id) references requests(id) on delete cascade)`,
		`insert into requests values('rsa','public','fingerprint',zeroblob(32),zeroblob(32),'submitted',3,zeroblob(32),zeroblob(32),'text',1,'{}','2026-10-08T00:00:00.000000000Z')`,
		`insert into request_payloads values('rsa',X'010203')`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(`select (select count(*) from requests) + (select count(*) from request_payloads)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("RSA requests survived the cut-over", count, err)
	}
	if _, err := conn.Exec(`insert into requests(id,public_key,fingerprint,submission_token_hash,retrieval_token_hash,expires_at) values('age','public','fingerprint',zeroblob(32),zeroblob(32),'2026-10-08T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into request_payloads(request_id,ciphertext) values('age',X'010203')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAPI(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select count(*) from requests r join request_payloads p on p.request_id=r.id`).Scan(&count); err != nil || count != 1 {
		t.Fatal("second migration dropped age requests", count, err)
	}
	if _, err := conn.Exec(`update requests set state='consumed',storage_backend='s3_object',kind='file',final_key='managed/requests/final',consumed_at=expires_at`); err != nil {
		t.Fatal("age schema columns or state CHECK absent", err)
	}
	if _, err := conn.Exec(`delete from requests`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select count(*) from request_payloads`).Scan(&count); err != nil || count != 0 {
		t.Fatal("FK cascade lost", err)
	}
	var foreignKeys int
	if err := conn.QueryRow(`pragma foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("FK enforcement not restored", err)
	}
}
