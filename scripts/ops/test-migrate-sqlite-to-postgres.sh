#!/usr/bin/env bash
# Regression harness for scripts/ops/migrate-sqlite-to-postgres.sh.
#
# Builds SQLite fixtures from the final SQLite release schemas
# (scripts/ops/testdata/sqlite-*-schema.sql, dumped from the old MigrateAPI /
# MigrateWorker), creates throwaway Postgres databases with the HEAD schema via
# `flick-api migrate` / `flick-worker migrate`, runs the cutover, and checks
# the result against values written by hand from the fixture, not values read
# back through the script. Then checks that the script refuses bad input.
#
# Needs: go, sqlite3, docker, xxd, and a running postgres:17 container
# (CUTOVER_TEST_CONTAINER, default flick-pr3-main-postgres-1) published on
# 127.0.0.1:CUTOVER_TEST_PG_PORT (default 15435) with superuser postgres /
# CUTOVER_TEST_PG_PASSWORD (default flick). It creates and drops the
# databases cutover_test_api and cutover_test_worker.
set -euo pipefail
export LC_ALL=C

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
SCRIPT=$ROOT/scripts/ops/migrate-sqlite-to-postgres.sh
TESTDATA=$ROOT/scripts/ops/testdata
CONTAINER=${CUTOVER_TEST_CONTAINER:-flick-pr3-main-postgres-1}
PG_PORT=${CUTOVER_TEST_PG_PORT:-15435}
PG_PASSWORD=${CUTOVER_TEST_PG_PASSWORD:-flick}
API_DB=cutover_test_api
WORKER_DB=cutover_test_worker

WORK=$(mktemp -d "${TMPDIR:-/tmp}/cutover-test.XXXXXX")
PASSED=0
FAILED=0

pg() { # db, then psql args; SQL on stdin or -c
	local db=$1
	shift
	docker exec -i -e PGCLIENTENCODING=UTF8 "$CONTAINER" psql -X -q -At -v ON_ERROR_STOP=1 -U postgres -d "$db" "$@"
}

drop_targets() {
	pg postgres -c "drop database if exists $API_DB with (force)" >/dev/null
	pg postgres -c "drop database if exists $WORKER_DB with (force)" >/dev/null
}

cleanup() {
	drop_targets || true
	rm -rf "$WORK"
}
trap cleanup EXIT

pass() { PASSED=$((PASSED + 1)); printf 'PASS  %s\n' "$1"; }
fail() { FAILED=$((FAILED + 1)); printf 'FAIL  %s\n' "$1"; }

check() { # name expected actual
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (expected [$2], got [$3])"; fi
}

# Empty HEAD-schema targets, created the way the runbook does it.
fresh_targets() {
	drop_targets
	pg postgres -c "create database $API_DB" >/dev/null
	pg postgres -c "create database $WORKER_DB" >/dev/null
	local dsn="postgres://postgres:$PG_PASSWORD@127.0.0.1:$PG_PORT"
	env -i PATH="$PATH" FLICK_DATABASE_URL="$dsn/$API_DB?sslmode=disable" "$WORK/flick-api" migrate >/dev/null 2>&1
	env -i PATH="$PATH" FLICK_WORKER_DATABASE_URL="$dsn/$WORKER_DB?sslmode=disable" "$WORK/flick-worker" migrate >/dev/null 2>&1
}

run_cutover() { # api.db worker.db [extra args]
	local api=$1 worker=$2
	shift 2
	"$SCRIPT" "$@" \
		--api-sqlite "$api" --api-psql "docker exec -i $CONTAINER psql -U postgres -d $API_DB" \
		--worker-sqlite "$worker" --worker-psql "docker exec -i $CONTAINER psql -U postgres -d $WORKER_DB"
}

expect_fail() { # name pattern command...
	local name=$1 pattern=$2 out rc=0
	shift 2
	out=$("$@" 2>&1) || rc=$?
	if [ "$rc" -ne 0 ] && grep -q -- "$pattern" <<<"$out"; then
		pass "$name (exit $rc)"
		grep -- "$pattern" <<<"$out" | head -1 | sed 's/^/      /'
	else
		fail "$name (exit $rc, wanted non-zero with /$pattern/)"
		printf '%s\n' "$out" | tail -8 | sed 's/^/      /'
	fi
}

hash32() { # 32 bytes of one repeated hex byte, as a SQLite blob literal
	local out=''
	for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32; do out=$out$1; done
	printf "x'%s'" "$out"
}

make_fixture() { # dir
	local dir=$1
	mkdir -p "$dir"
	sqlite3 "$dir/api.db" 'pragma journal_mode = wal' >/dev/null
	sqlite3 "$dir/worker.db" 'pragma journal_mode = wal' >/dev/null
	sqlite3 -bail "$dir/api.db" <"$TESTDATA/sqlite-api-schema.sql"
	sqlite3 -bail "$dir/worker.db" <"$TESTDATA/sqlite-worker-schema.sql"
	sqlite3 -bail "$dir/api.db" <<EOF
pragma foreign_keys = on;
insert into object_reconciliation_cursor (id, continuation_token, generation) values (1, 'legacy-token', 4);
insert into object_reconciliation_pending (object_key, job_id) values ('legacy/key', 'legacy-job');
insert into request_reconciliation_cursor (id) values (1);
insert into object_reconciliation_cursor_v2 values ('s3_object', 'tok/abc==', 3), ('r2_object', '', 0);
insert into request_reconciliation_cursor_v2 values ('s3_object', '', 0), ('r2_object', 'r2-token', 1);
insert into object_reconciliation_pending_v2 values ('s3_object', 'managed/secrets/orphan-1', 'job-o1'), ('r2_object', 'managed/secrets/orphan-1', 'job-o2');
insert into request_reconciliation_pending_v2 values ('s3_object', 'requests/x/final', 'job-r1');

insert into secrets (id, kind, storage_backend, storage_key, access_kdf_params_json, access_proof_hash, wrapped_identity,
	encrypted_filename, content_type, size_bytes, max_views, view_count, failed_access_count, state,
	expires_at, consumed_at, created_at, updated_at, reclaim_enqueued_at) values
('s-inline', 'text', 'sqlite_blob', 'inline/s-inline', null, null, null, null, '', 13, 1, 0, 0, 'active',
	'2026-11-16T10:00:00.123456789Z', null, '2026-11-16T09:00:00.5Z', '2026-11-16T09:00:00Z', null),
('s-modela', 'file', 's3_object', 'managed/secrets/s-modela', '{"n":"a,b","s":"x\"y"}', 'proof+/=',
	'-----BEGIN AGE ENCRYPTED FILE-----' || char(10) || 'YWdl' || char(10) || '-----END AGE ENCRYPTED FILE-----',
	'ZmlsZSwgIm5hbWUi', 'application/octet-stream', 1048576, 3, 1, 2, 'pending_upload',
	'2026-11-17T00:00:00.000000000Z', null, '2026-11-16T09:30:00.000000001Z', '2026-11-16T09:31:00.999999999Z', '2026-11-16T09:40:00.25Z'),
('s-locked', 'text', 'r2_object', 'managed/secrets/s-locked', null, null, null, null, null, 0, 1, 0, 5, 'active',
	'2026-11-17T00:00:00Z', '2026-11-16T11:00:00.000001Z', '2026-11-16T09:00:01Z', '2026-11-16T11:00:00.000001Z', null),
('s-big', 'file', 'sqlite_blob', 'inline/s-big', null, null, null, 'メモ 한국어 ✓' || char(9) || '|x' || char(13), 'text/plain', 70000, 2, 0, 0, 'active',
	'2026-11-17T00:00:00Z', null, '2026-11-16T09:00:02Z', '2026-11-16T09:00:02Z', null);

insert into secret_payloads values
('s-inline', x'00ff80c3280a0d2c22005c2e7c', '2026-11-16T09:00:00.5Z'),
('s-locked', x'', '2026-11-16T09:00:01Z'),
('s-big', randomblob(70000), '2026-11-16T09:00:02.000000000Z');

insert into secret_management values
('s-inline', x'00ff000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d', '2026-11-23T09:00:00.000000000Z', null),
('s-modela', $(hash32 ff), '2026-11-23T09:30:00.000000000Z', 'locked'),
('s-gone', $(hash32 00), '2026-11-23T08:00:00.000000000Z', 'cancelled');

insert into secret_open_grace values ('managed/secrets/opened-1', '2026-11-16T10:35:00.123000000Z');

insert into usage_counters values ('2026-11-15', 'secret_created_text', 5), ('2026-11-16', 'secret_created_text', 7), ('2026-11-16', 'bytes_uploaded', 1048589);

insert into requests (id, public_key, fingerprint, encrypted_note, submission_token_hash, retrieval_token_hash, state, generation,
	attempt_token_hash, attempt_body_hash, kind, size_bytes, envelope_json, storage_backend, upload_key, final_key,
	ciphertext_sha256, reservation_expires_at, consumed_at, expires_at) values
('r-waiting', 'age1pubkey', 'fp-1', null, $(hash32 11), $(hash32 12), 'waiting', 1,
	null, null, null, null, null, 'sqlite_blob', null, null, null, null, null, '2026-11-18T00:00:00.000000000Z'),
('r-submitted', 'age1pubkey2', 'fp-2', 'bm90ZQ==', $(hash32 21), $(hash32 22), 'submitted', 2,
	$(hash32 23), $(hash32 24), 'text', 42, '{"kind":"text","name":"메모"}', 'sqlite_blob', null, null, null, null, null, '2026-11-18T00:00:00.000000000Z'),
('r-uploading', 'age1pubkey3', 'fp-3', null, $(hash32 31), $(hash32 32), 'uploading', 1,
	$(hash32 33), $(hash32 34), 'file', 5000000, '{"kind":"file"}', 's3_object', 'requests/r-uploading/upload', 'requests/r-uploading/final',
	$(hash32 35), '2026-11-16T10:15:00.000000000Z', null, '2026-11-18T00:00:00.000000000Z'),
('r-consumed', 'age1pubkey4', 'fp-4', null, $(hash32 41), $(hash32 42), 'consumed', 3,
	null, null, null, null, null, 'r2_object', null, 'requests/r-consumed/final', null, null, '2026-11-16T10:20:00.000000000Z', '2026-11-18T00:00:00.000000000Z');

insert into request_payloads values ('r-submitted', x'000102fffe');

insert into outbox_events values
('ev-pending', 'flick.jobs', '{"job_id":"ev-pending"}', 'pending', 0, '2026-11-16T10:00:05.123Z', null, null, '2026-11-16T10:00:00Z', '2026-11-16T10:00:00Z'),
('ev-failed', 'flick.jobs', '{"job_id":"ev-failed"}', 'failed', 7, '2026-11-16T10:00:05Z', null, '', '2026-11-16T10:00:00Z', '2026-11-16T10:30:00Z'),
('ev-published', 'flick.jobs', '{"job_id":"ev-published"}', 'published', 1, '2026-11-16T10:00:05Z', '2026-11-16T10:01:00.000000000Z', null, '2026-11-16T10:00:00Z', '2026-11-16T10:01:00Z');
EOF
	sqlite3 -bail "$dir/worker.db" <<'EOF'
pragma foreign_keys = on;
insert into job_receipts values
('job-1', 'object_delete', 'succeeded', 1, null, '2026-11-16T10:00:00.1Z', '2026-11-16T10:00:01Z', '2026-11-16T10:00:01Z'),
('job-2', 'object_delete', 'dead', 3, 'boom: 500', '2026-11-16T10:00:00Z', '2026-11-16T10:05:00.000000000Z', null);
insert into job_attempts (id, job_id, attempt, started_at, finished_at, result, error) values
(1, 'job-1', 1, '2026-11-16T10:00:00.1Z', '2026-11-16T10:00:01Z', 'succeeded', null),
(2, 'job-2', 1, '2026-11-16T10:00:00Z', '2026-11-16T10:00:02Z', 'failed', 'boom'),
(5, 'job-2', 2, '2026-11-16T10:04:00Z', null, 'running', '');
-- Rows 6 and 7 existed and were deleted; AUTOINCREMENT never reuses them.
update sqlite_sequence set seq = 7 where name = 'job_attempts';
insert into dead_letters values ('job-2', 'object_delete', '{"job_id":"job-2","note":"한국어 ✓"}', 'boom: 500', '2026-11-16T10:05:00.000000000Z');
EOF
	# Production files arrive checkpointed (PRAGMA wal_checkpoint(TRUNCATE)).
	sqlite3 "$dir/api.db" 'pragma wal_checkpoint(truncate)' >/dev/null
	sqlite3 "$dir/worker.db" 'pragma wal_checkpoint(truncate)' >/dev/null
}

# ---------------------------------------------------------------------------
printf 'Building flick-api and flick-worker from %s\n' "$ROOT"
(cd "$ROOT" && go build -o "$WORK/flick-api" ./cmd/flick-api && go build -o "$WORK/flick-worker" ./cmd/flick-worker)
make_fixture "$WORK/good"

# 1. Happy path
fresh_targets
out=$(run_cutover "$WORK/good/api.db" "$WORK/good/worker.db" 2>&1) && rc=0 || rc=$?
printf '%s\n' "$out" | sed 's/^/      /'
if [ "$rc" = 0 ] && grep -q '^OK: every table matches SQLite' <<<"$out"; then pass 'cutover loads and verifies'; else fail "cutover loads and verifies (exit $rc)"; fi

# 2. Loaded values, against literals from the fixture above
check 'all 12 api tables loaded, legacy v1 tables absent' \
	'3|1|3|2|1|2|4|1|2|1|3|3|true' \
	"$(pg $API_DB -c "select (select count(*) from secrets where state = 'active') || '|' || (select count(*) from secret_open_grace) || '|' ||
		(select count(*) from secret_management) || '|' || (select count(*) from object_reconciliation_pending_v2) || '|' ||
		(select count(*) from request_reconciliation_pending_v2) || '|' || (select count(*) from object_reconciliation_cursor_v2) || '|' ||
		(select count(*) from requests) || '|' || (select count(*) from request_payloads) || '|' || (select count(*) from request_reconciliation_cursor_v2) || '|' ||
		(select count(*) from secret_payloads where length(ciphertext) = 0) || '|' || (select count(*) from usage_counters) || '|' ||
		(select count(*) from outbox_events) || '|' || (to_regclass('object_reconciliation_cursor') is null and to_regclass('request_reconciliation_pending') is null)")"
check 'NULL and empty string round-trip separately' 'true|true|true|true' \
	"$(pg $API_DB -c "select (select content_type = '' and encrypted_filename is null from secrets where id = 's-inline') || '|' ||
		(select last_error is null from outbox_events where id = 'ev-pending') || '|' || (select last_error = '' from outbox_events where id = 'ev-failed') || '|' ||
		(select encrypted_note is null and attempt_token_hash is null from requests where id = 'r-waiting')")"
check 'job_attempts NULL/empty error (worker db)' 'true|true' \
	"$(pg $WORKER_DB -c "select (select error = '' from job_attempts where id = 5) || '|' || (select error is null from job_attempts where id = 1)")"
check 'binary blob with 0x00 and invalid UTF-8 is byte-exact' '00ff80c3280a0d2c22005c2e7c|000102fffe|00ff000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d' \
	"$(pg $API_DB -c "select (select encode(ciphertext, 'hex') from secret_payloads where secret_id = 's-inline') || '|' ||
		(select encode(ciphertext, 'hex') from request_payloads where request_id = 'r-submitted') || '|' ||
		(select encode(token_hash, 'hex') from secret_management where secret_id = 's-inline')")"
big_sqlite=$(sqlite3 "$WORK/good/api.db" "select hex(ciphertext) from secret_payloads where secret_id = 's-big'" | xxd -r -p | shasum -a 256 | cut -d' ' -f1)
check '70000-byte random blob SHA-256 matches' "$big_sqlite" \
	"$(pg $API_DB -c "select encode(sha256(ciphertext), 'hex') from secret_payloads where secret_id = 's-big'")"
check 'multi-byte, newline, tab, CR, quote, comma, backslash text is exact' 'true|true|true|true' \
	"$(pg $API_DB -c "select (select encrypted_filename = E'メモ 한국어 ✓\t|x\r' from secrets where id = 's-big') || '|' ||
		(select wrapped_identity = E'-----BEGIN AGE ENCRYPTED FILE-----\nYWdl\n-----END AGE ENCRYPTED FILE-----' and access_kdf_params_json = '{\"n\":\"a,b\",\"s\":\"x\\\"y\"}' from secrets where id = 's-modela') || '|' ||
		(select envelope_json = '{\"kind\":\"text\",\"name\":\"메모\"}' from requests where id = 'r-submitted') || '|' ||
		(select count(*) = 1 from usage_counters where day = '2026-11-16' and metric = 'bytes_uploaded' and count = 1048589)")"
check 'dead_letters UTF-8 payload is exact' 't' \
	"$(pg $WORKER_DB -c "select payload_json = '{\"job_id\":\"job-2\",\"note\":\"한국어 ✓\"}' from dead_letters")"
check 'timestamps keep microseconds and truncate nanoseconds (never round up)' \
	'2026-11-16T10:00:00.123456|2026-11-16T09:00:00.500000|2026-11-16T09:30:00.000000|2026-11-16T09:31:00.999999|2026-11-16T10:35:00.123000|2026-11-16T10:20:00.000000' \
	"$(pg $API_DB -c "set time zone 'Asia/Seoul'; select (select to_char(expires_at at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') || '|' || to_char(created_at at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') from secrets where id = 's-inline') || '|' ||
		(select to_char(created_at at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') || '|' || to_char(updated_at at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') from secrets where id = 's-modela') || '|' ||
		(select to_char(grace_until at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') from secret_open_grace) || '|' ||
		(select to_char(consumed_at at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US') from requests where id = 'r-consumed')")"
check 'reconciliation cursors replace the migrate seeds' 'r2_object::0|s3_object:tok/abc==:3|r2_object:r2-token:1|s3_object::0' \
	"$(pg $API_DB -c "select string_agg(backend || ':' || continuation_token || ':' || generation, '|' order by backend) from object_reconciliation_cursor_v2 union all
		select string_agg(backend || ':' || continuation_token || ':' || generation, '|' order by backend) from request_reconciliation_cursor_v2" | paste -sd'|' -)"
check 'new job_attempts row takes id 8 (past MAX(id)=5 and sqlite_sequence=7), no 23505' '8' \
	"$(pg $WORKER_DB -c "insert into job_attempts (job_id, attempt, started_at, result) values ('job-1', 2, now(), 'running') returning id")"

# 3. Refusals
expect_fail '(a) refuses a non-empty target' 'refusing to load into a non-empty database' \
	run_cutover "$WORK/good/api.db" "$WORK/good/worker.db"
check '(a) refused run left the loaded rows untouched' '4|4' \
	"$(pg $API_DB -c "select (select count(*) from secrets) || '|' || (select count(*) from requests)")"

pg $API_DB -c "update secret_payloads set ciphertext = overlay(ciphertext placing '\\x01'::bytea from 1 for 1) where secret_id = 's-big'" >/dev/null
expect_fail '(e) --verify-only catches one changed payload byte' 'secret_payloads .*MISMATCH' \
	run_cutover "$WORK/good/api.db" "$WORK/good/worker.db" --verify-only --only api

make_fixture "$WORK/missing"
sqlite3 "$WORK/missing/api.db" 'alter table requests drop column encrypted_note'
fresh_targets
expect_fail '(b) refuses a SQLite table with a missing column' 'requests: SQLite columns are not the final SQLite release schema' \
	run_cutover "$WORK/missing/api.db" "$WORK/missing/worker.db"

make_fixture "$WORK/extra"
sqlite3 "$WORK/extra/api.db" 'alter table secrets add column extra text'
expect_fail '(c) refuses a SQLite table with an unknown column' 'secrets: SQLite columns are not the final SQLite release schema' \
	run_cutover "$WORK/extra/api.db" "$WORK/extra/worker.db"

make_fixture "$WORK/badtime"
sqlite3 "$WORK/badtime/worker.db" "update job_receipts set updated_at = '2026-11-16 10:00:01' where job_id = 'job-1'"
expect_fail '(d) refuses a timestamp outside the RFC 3339 UTC format' 'job_receipts.updated_at: 1 value(s)' \
	run_cutover "$WORK/badtime/api.db" "$WORK/badtime/worker.db"

make_fixture "$WORK/nul"
sqlite3 "$WORK/nul/worker.db" "update dead_letters set error = 'a' || char(0) || 'b'"
expect_fail '(f) a row Postgres rejects rolls the whole worker load back' 'load failed .* rolled back' \
	run_cutover "$WORK/nul/api.db" "$WORK/nul/worker.db" --only worker
check '(f) worker target still empty after the rollback' '0|0' \
	"$(pg $WORKER_DB -c "select (select count(*) from job_receipts) || '|' || (select count(*) from job_attempts)")"
check 'refused runs never wrote to the api target' '0|2' \
	"$(pg $API_DB -c "select (select count(*) from secrets) || '|' || (select count(*) from object_reconciliation_cursor_v2 where continuation_token = '')")"

printf '\n%d passed, %d failed\n' "$PASSED" "$FAILED"
[ "$FAILED" = 0 ]
