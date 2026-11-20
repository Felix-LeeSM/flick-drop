#!/usr/bin/env bash
# The object reconciler deletes objects no row references, so a partial flick_api DB deletes live
# objects: refuse any drift or non-empty target, and never print payloads, tokens, or per-row hashes.
set -euo pipefail
export LC_ALL=C

usage() {
	cat <<'EOF'
Usage: migrate-sqlite-to-postgres.sh [mode] [--only api|worker] \
         --api-sqlite PATH --api-psql 'CMD' \
         --worker-sqlite PATH --worker-psql 'CMD'

Modes (default: check, load, verify):
  --check-only    run the SQLite and Postgres pre-flight checks, write nothing
  --verify-only   compare already-loaded Postgres data against SQLite

--api-psql / --worker-psql take a psql command prefix that connects to the
target database; the script appends its own flags (-X -q -v ON_ERROR_STOP=1)
and feeds SQL on stdin. Examples:
  --api-psql 'psql postgres://flick_api@127.0.0.1:5432/flick_api'
  --api-psql 'kubectl -n flick exec -i deploy/postgres -- psql -U flick_api -d flick_api'
The prefix is split on whitespace; put passwords in PGPASSWORD or ~/.pgpass,
not in the command.
EOF
}

die() {
	printf 'migrate-sqlite-to-postgres: FAIL: %s\n' "$*" >&2
	exit 1
}

# Fields: db table column sqlite_decl_type pg_type sqlite_notnull pk_position. Must match
# testdata/sqlite-*-schema.sql and internal/db migrations; pg_type "-" = legacy, checked not loaded.
SPEC='
api secrets id text text 0 1
api secrets kind text text 1 0
api secrets storage_backend text text 1 0
api secrets storage_key text text 1 0
api secrets access_kdf_params_json text text 0 0
api secrets access_proof_hash text text 0 0
api secrets wrapped_identity text text 0 0
api secrets encrypted_filename text text 0 0
api secrets content_type text text 0 0
api secrets size_bytes integer bigint 1 0
api secrets max_views integer integer 1 0
api secrets view_count integer integer 1 0
api secrets failed_access_count integer integer 1 0
api secrets state text text 1 0
api secrets expires_at datetime timestamptz 1 0
api secrets consumed_at datetime timestamptz 0 0
api secrets created_at datetime timestamptz 1 0
api secrets updated_at datetime timestamptz 1 0
api secrets reclaim_enqueued_at datetime timestamptz 0 0
api secret_payloads secret_id text text 0 1
api secret_payloads ciphertext blob bytea 1 0
api secret_payloads created_at datetime timestamptz 1 0
api secret_management secret_id text text 0 1
api secret_management token_hash blob bytea 1 0
api secret_management expires_at datetime timestamptz 1 0
api secret_management outcome text text 0 0
api secret_open_grace object_key text text 0 1
api secret_open_grace grace_until datetime timestamptz 1 0
api object_reconciliation_cursor_v2 backend text text 0 1
api object_reconciliation_cursor_v2 continuation_token text text 1 0
api object_reconciliation_cursor_v2 generation integer integer 1 0
api object_reconciliation_pending_v2 backend text text 1 1
api object_reconciliation_pending_v2 object_key text text 1 2
api object_reconciliation_pending_v2 job_id text text 1 0
api requests id text text 0 1
api requests public_key text text 1 0
api requests fingerprint text text 1 0
api requests encrypted_note text text 0 0
api requests submission_token_hash blob bytea 1 0
api requests retrieval_token_hash blob bytea 1 0
api requests state text text 1 0
api requests generation integer integer 1 0
api requests attempt_token_hash blob bytea 0 0
api requests attempt_body_hash blob bytea 0 0
api requests kind text text 0 0
api requests size_bytes integer bigint 0 0
api requests envelope_json text text 0 0
api requests storage_backend text text 1 0
api requests upload_key text text 0 0
api requests final_key text text 0 0
api requests ciphertext_sha256 blob bytea 0 0
api requests reservation_expires_at text timestamptz 0 0
api requests consumed_at text timestamptz 0 0
api requests expires_at text timestamptz 1 0
api request_payloads request_id text text 0 1
api request_payloads ciphertext blob bytea 1 0
api request_reconciliation_cursor_v2 backend text text 0 1
api request_reconciliation_cursor_v2 continuation_token text text 1 0
api request_reconciliation_cursor_v2 generation integer integer 1 0
api request_reconciliation_pending_v2 backend text text 1 1
api request_reconciliation_pending_v2 object_key text text 1 2
api request_reconciliation_pending_v2 job_id text text 1 0
api outbox_events id text text 0 1
api outbox_events subject text text 1 0
api outbox_events payload_json text text 1 0
api outbox_events state text text 1 0
api outbox_events attempts integer integer 1 0
api outbox_events next_attempt_at datetime timestamptz 1 0
api outbox_events published_at datetime timestamptz 0 0
api outbox_events last_error text text 0 0
api outbox_events created_at datetime timestamptz 1 0
api outbox_events updated_at datetime timestamptz 1 0
api usage_counters day text text 1 1
api usage_counters metric text text 1 2
api usage_counters count integer bigint 1 0
api object_reconciliation_cursor id integer - 0 1
api object_reconciliation_cursor continuation_token text - 1 0
api object_reconciliation_cursor generation integer - 1 0
api object_reconciliation_pending object_key text - 0 1
api object_reconciliation_pending job_id text - 1 0
api request_reconciliation_cursor id integer - 0 1
api request_reconciliation_cursor continuation_token text - 1 0
api request_reconciliation_cursor generation integer - 1 0
api request_reconciliation_pending object_key text - 0 1
api request_reconciliation_pending job_id text - 1 0
worker job_receipts job_id text text 0 1
worker job_receipts kind text text 1 0
worker job_receipts state text text 1 0
worker job_receipts attempts integer integer 1 0
worker job_receipts last_error text text 0 0
worker job_receipts first_seen_at datetime timestamptz 1 0
worker job_receipts updated_at datetime timestamptz 1 0
worker job_receipts completed_at datetime timestamptz 0 0
worker job_attempts id integer bigint 0 1
worker job_attempts job_id text text 1 0
worker job_attempts attempt integer integer 1 0
worker job_attempts started_at datetime timestamptz 1 0
worker job_attempts finished_at datetime timestamptz 0 0
worker job_attempts result text text 1 0
worker job_attempts error text text 0 0
worker dead_letters job_id text text 0 1
worker dead_letters kind text text 1 0
worker dead_letters payload_json text text 1 0
worker dead_letters error text text 1 0
worker dead_letters created_at datetime timestamptz 1 0
'

# MigrateAPI seeds one ('', 0) row per backend; the load replaces them.
SEEDED_TABLES='object_reconciliation_cursor_v2 request_reconciliation_cursor_v2'

# label|sqlite expression|postgres expression
AGGREGATES_api='
secrets SUM(size_bytes)|select coalesce(sum(size_bytes), 0) from secrets|select coalesce(sum(size_bytes), 0) from secrets
secrets state=active|select count(*) from secrets where state = '"'"'active'"'"'|select count(*) from secrets where state = '"'"'active'"'"'
secret_payloads SUM(length)|select coalesce(sum(length(ciphertext)), 0) from secret_payloads|select coalesce(sum(length(ciphertext)), 0) from secret_payloads
requests SUM(size_bytes)|select coalesce(sum(size_bytes), 0) from requests|select coalesce(sum(size_bytes), 0) from requests
request_payloads SUM(length)|select coalesce(sum(length(ciphertext)), 0) from request_payloads|select coalesce(sum(length(ciphertext)), 0) from request_payloads
usage_counters SUM(count)|select coalesce(sum(count), 0) from usage_counters|select coalesce(sum(count), 0) from usage_counters
'
AGGREGATES_worker='
job_attempts MAX(id)|select coalesce(max(id), 0) from job_attempts|select coalesce(max(id), 0) from job_attempts
'

MODE=all
ONLY=
API_SQLITE=
WORKER_SQLITE=
API_PSQL=
WORKER_PSQL=
while [ $# -gt 0 ]; do
	case "$1" in
	--check-only) MODE=check ;;
	--verify-only) MODE=verify ;;
	--only) ONLY=${2:?--only needs api or worker}; shift ;;
	--api-sqlite) API_SQLITE=${2:?}; shift ;;
	--worker-sqlite) WORKER_SQLITE=${2:?}; shift ;;
	--api-psql) API_PSQL=${2:?}; shift ;;
	--worker-psql) WORKER_PSQL=${2:?}; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown argument: $1" ;;
	esac
	shift
done

case "$ONLY" in
'') DATABASES='api worker' ;;
api | worker) DATABASES=$ONLY ;;
*) die "--only must be api or worker, got: $ONLY" ;;
esac
for db in $DATABASES; do
	if [ "$db" = api ]; then
		[ -n "$API_SQLITE" ] && [ -n "$API_PSQL" ] || die "--api-sqlite and --api-psql are required"
	else
		[ -n "$WORKER_SQLITE" ] && [ -n "$WORKER_PSQL" ] || die "--worker-sqlite and --worker-psql are required"
	fi
done

command -v sqlite3 >/dev/null || die "sqlite3 is not on PATH"
if command -v sha256sum >/dev/null; then
	sha256() { sha256sum | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then
	sha256() { shasum -a 256 | cut -d' ' -f1; }
else
	die "neither sha256sum nor shasum is on PATH"
fi

sqlite_path() { if [ "$1" = api ]; then printf '%s' "$API_SQLITE"; else printf '%s' "$WORKER_SQLITE"; fi; }

sqlite_q() {
	sqlite3 -readonly -batch -bail -noheader -list -separator , -nullvalue '' "$(sqlite_path "$1")" "$2"
}

run_psql() {
	local db=$1 line
	shift
	if [ "$db" = api ]; then line=$API_PSQL; else line=$WORKER_PSQL; fi
	local -a cmd
	read -r -a cmd <<<"$line"
	"${cmd[@]}" -X -q -v ON_ERROR_STOP=1 "$@"
}

psql_q() { printf '%s\n' "$2" | run_psql "$1" -At; }

spec_tables() { # db [loaded|legacy|all]
	printf '%s\n' "$SPEC" | awk -v db="$1" -v which="${2:-all}" '
		$1 == db && !seen[$2]++ {
			legacy = ($5 == "-")
			if (which == "all" || (which == "legacy" && legacy) || (which == "loaded" && !legacy)) print $2
		}'
}

spec_columns() { # db table -> "column sqlite_type pg_type notnull pk"
	printf '%s\n' "$SPEC" | awk -v db="$1" -v t="$2" '$1 == db && $2 == t { print $3, $4, $5, $6, $7 }'
}

is_seeded() { case " $SEEDED_TABLES " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

kind_of() { # pg_type -> t|i|b|ts
	case "$1" in
	text) echo t ;;
	integer | bigint) echo i ;;
	bytea) echo b ;;
	timestamptz) echo ts ;;
	*) die "spec: unknown pg type $1" ;;
	esac
}

join_by() { # sep words...
	local sep=$1 out='' w
	shift
	for w in "$@"; do out=${out:+$out$sep}$w; done
	printf '%s' "$out"
}

quoted_columns() {
	local col rest out=''
	while read -r col rest; do out=${out:+$out, }\"$col\"; done < <(spec_columns "$1" "$2")
	printf '%s' "$out"
}

# Text keys sort bytewise on both sides (SQLite BINARY, Postgres "C").
order_by() { # db table engine
	spec_columns "$1" "$2" | awk '$5 > 0' | sort -k5,5n | while read -r col _ pg rest; do
		if [ "$3" = pg ] && [ "$(kind_of "$pg")" = t ]; then printf '"%s" collate "C"\n' "$col"; else printf '"%s"\n' "$col"; fi
	done | paste -sd, -
}

# RFC 3339 UTC "Z" with 0-9 fraction digits, as the last SQLite release wrote.
ts_ok_sqlite() {
	local c=$1 d='[0-9]'
	local base="$d$d$d$d-$d$d-$d${d}T$d$d:$d$d:$d$d"
	printf "(typeof(%s) = 'text' and (%s glob '%sZ' or (%s glob '%s.*Z' and length(%s) between 22 and 30 and substr(%s, 21, length(%s) - 21) not glob '*[^0-9]*')))" \
		"$c" "$c" "$base" "$c" "$base" "$c" "$c" "$c"
}

# SQLite hex(NULL) returns '', so every hex() of a nullable column sits behind a NULL test.
# Timestamps render with exactly 6 fraction digits (the load truncates ns to Postgres us).
canonical_sqlite() {
	local col pg rest c parts=()
	while read -r col _ pg rest; do
		c="\"$col\""
		case "$(kind_of "$pg")" in
		t | b) parts+=("case when $c is null then 'N' else 'x' || lower(hex($c)) end") ;;
		i) parts+=("coalesce(cast($c as text), 'N')") ;;
		ts) parts+=("coalesce(substr($c, 1, 19) || '.' || substr((case when substr($c, 20, 1) = '.' then substr($c, 21, length($c) - 21) else '' end) || '000000', 1, 6) || 'Z', 'N')") ;;
		esac
	done < <(spec_columns "$1" "$2")
	join_by " || '|' || " "${parts[@]}"
}

canonical_pg() {
	local col pg rest c parts=()
	while read -r col _ pg rest; do
		c="\"$col\""
		case "$(kind_of "$pg")" in
		t) parts+=("coalesce('x' || encode(convert_to($c, 'UTF8'), 'hex'), 'N')") ;;
		b) parts+=("coalesce('x' || encode($c, 'hex'), 'N')") ;;
		i) parts+=("coalesce($c::text, 'N')") ;;
		ts) parts+=("coalesce(to_char($c at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), 'N')") ;;
		esac
	done < <(spec_columns "$1" "$2")
	join_by " || '|' || " "${parts[@]}"
}

digest_sqlite() {
	sqlite_q "$1" "select $(canonical_sqlite "$1" "$2") from \"$2\" order by $(order_by "$1" "$2" sqlite)" | sha256
}

digest_pg() {
	printf 'COPY (select %s from "%s" order by %s) TO STDOUT;\n' "$(canonical_pg "$1" "$2")" "$2" "$(order_by "$1" "$2" pg)" |
		run_psql "$1" | sha256
}

pg_nonempty_count_sql() {
	if is_seeded "$1"; then
		printf "select count(*) from \"%s\" where not (backend in ('s3_object', 'r2_object') and continuation_token = '' and generation = 0)" "$1"
	else
		printf 'select count(*) from "%s"' "$1"
	fi
}

check_sqlite() {
	local db=$1 path t col pg nn pk bad
	path=$(sqlite_path "$db")
	[ -f "$path" ] && [ -r "$path" ] || die "$db: SQLite file not readable: $path"
	if [ -s "$path-wal" ]; then
		die "$db: $path-wal is not empty; run PRAGMA wal_checkpoint(TRUNCATE) on the stopped database and copy again"
	fi
	[ "$(sqlite_q "$db" 'pragma encoding')" = UTF-8 ] || die "$db: SQLite encoding is not UTF-8"
	[ "$(sqlite_q "$db" 'pragma integrity_check')" = ok ] || die "$db: PRAGMA integrity_check failed"
	[ -z "$(sqlite_q "$db" 'pragma foreign_key_check')" ] || die "$db: PRAGMA foreign_key_check reports orphan rows"

	local expected actual
	expected=$(spec_tables "$db" | sort)
	actual=$(sqlite_q "$db" "select name from sqlite_master where type = 'table' and name not like 'sqlite\_%' escape '\\'" | sort)
	if [ "$expected" != "$actual" ]; then
		printf '%s: table set differs from the final SQLite release (< expected, > found):\n' "$db" >&2
		diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | grep '^[<>]' >&2 || true
		die "$db: SQLite schema is not the final SQLite release schema"
	fi

	for t in $(spec_tables "$db"); do
		expected=$(spec_columns "$db" "$t" | awk '{ print $1, $2, $4, $5 }' | sort)
		actual=$(sqlite_q "$db" "select lower(name) || ' ' || lower(type) || ' ' || \"notnull\" || ' ' || pk from pragma_table_info('$t')" | sort)
		if [ "$expected" != "$actual" ]; then
			printf '%s.%s: columns differ (name type notnull pk; < expected, > found):\n' "$db" "$t" >&2
			diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | grep '^[<>]' >&2 || true
			die "$db.$t: SQLite columns are not the final SQLite release schema"
		fi
	done

	# A value of another shape would be silently reinterpreted by the load.
	for t in $(spec_tables "$db" loaded); do
		local cols=() checks=()
		while read -r col _ pg nn pk; do
			local c="\"$col\"" cond
			case "$(kind_of "$pg")" in
			t) cond="typeof($c) = 'text'" ;;
			i) cond="typeof($c) = 'integer'" ;;
			b) cond="typeof($c) = 'blob'" ;;
			ts) cond=$(ts_ok_sqlite "$c") ;;
			esac
			if [ "$nn" = 1 ] || [ "$pk" != 0 ]; then
				checks+=("sum(case when $c is null or not $cond then 1 else 0 end)")
			else
				checks+=("sum(case when $c is not null and not $cond then 1 else 0 end)")
			fi
			cols+=("$col")
		done < <(spec_columns "$db" "$t")
		local counts i=0 n
		counts=$(sqlite_q "$db" "select $(join_by ', ' "${checks[@]}") from \"$t\"")
		IFS=, read -r -a bad <<<"$counts"
		for n in "${bad[@]}"; do
			if [ -n "$n" ] && [ "$n" != 0 ]; then
				die "$db.$t.${cols[$i]}: $n value(s) have an unexpected type, NULL, or timestamp format"
			fi
			i=$((i + 1))
		done
	done
	printf '%s: SQLite schema and value formats match the final SQLite release (%s)\n' "$db" "$path"
}

check_target() {
	local db=$1 expected actual t n
	psql_q "$db" 'select 1' >/dev/null || die "$db: cannot connect with the --$db-psql command"
	[ "$(psql_q "$db" 'show server_encoding')" = UTF8 ] || die "$db: Postgres server_encoding is not UTF8"

	expected=$(spec_tables "$db" loaded | sort)
	actual=$(psql_q "$db" "select table_name from information_schema.tables where table_schema = current_schema() and table_type = 'BASE TABLE'" | sort)
	if [ "$expected" != "$actual" ]; then
		printf '%s: Postgres table set differs (< expected, > found):\n' "$db" >&2
		diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | grep '^[<>]' >&2 || true
		die "$db: target is not the HEAD schema; run flick-$db migrate against an empty database first"
	fi

	for t in $(spec_tables "$db" loaded); do
		expected=$(spec_columns "$db" "$t" | awk '{
			pg = ($3 == "timestamptz") ? "timestamp_with_time_zone" : $3
			print $1, pg, ($4 == 1 || $5 > 0) ? "NO" : "YES" }' | sort)
		actual=$(psql_q "$db" "select column_name || ' ' || replace(data_type, ' ', '_') || ' ' || is_nullable from information_schema.columns where table_schema = current_schema() and table_name = '$t'" | sort)
		if [ "$expected" != "$actual" ]; then
			printf '%s.%s: Postgres columns differ (name type nullable; < expected, > found):\n' "$db" "$t" >&2
			diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | grep '^[<>]' >&2 || true
			die "$db.$t: target columns are not the HEAD schema"
		fi
		n=$(psql_q "$db" "$(pg_nonempty_count_sql "$t")")
		[ "$n" = 0 ] || die "$db.$t: target table already holds $n row(s); refusing to load into a non-empty database"
	done
	printf '%s: Postgres target has the HEAD schema and no data\n' "$db"
}

emit_load_sql() {
	local db=$1 t col pg rest c tables=() checks=()
	for t in $(spec_tables "$db" loaded); do
		tables+=("\"$t\"")
		checks+=("($(pg_nonempty_count_sql "$t")) > 0")
	done
	printf 'BEGIN;\n'
	printf 'LOCK TABLE %s IN ACCESS EXCLUSIVE MODE;\n' "$(join_by ', ' "${tables[@]}")"
	printf "DO \$cutover\$ BEGIN IF %s THEN RAISE EXCEPTION 'cutover: a target table is not empty'; END IF; END \$cutover\$;\n" \
		"$(join_by ' or ' "${checks[@]}")"
	for t in $SEEDED_TABLES; do
		case " $(spec_tables "$db" loaded | tr '\n' ' ') " in *" $t "*) printf 'DELETE FROM "%s";\n' "$t" ;; esac
	done

	for t in $(spec_tables "$db" loaded); do
		local stg="cutover_stg_$t" stg_cols=() exports=() convert=() expected
		while read -r col _ pg rest; do
			c="\"$col\""
			stg_cols+=("$c text")
			case "$(kind_of "$pg")" in
			t)
				exports+=("case when $c is null then null else 'x' || hex($c) end")
				convert+=("convert_from(decode(substr($c, 2), 'hex'), 'UTF8')")
				;;
			b)
				exports+=("case when $c is null then null else 'x' || hex($c) end")
				convert+=("decode(substr($c, 2), 'hex')")
				;;
			i)
				exports+=("$c")
				convert+=("$c::bigint")
				;;
			ts)
				# Truncate, never round, so the stored instant matches the digest.
				exports+=("$c")
				convert+=("regexp_replace($c, '^([0-9-]{10}T[0-9:]{8}(\\.[0-9]{1,6})?)[0-9]*Z\$', '\\1Z')::timestamptz")
				;;
			esac
		done < <(spec_columns "$db" "$t")
		expected=$(sqlite_q "$db" "select count(*) from \"$t\"") || exit 1

		printf 'CREATE TEMP TABLE "%s" (%s) ON COMMIT DROP;\n' "$stg" "$(join_by ', ' "${stg_cols[@]}")"
		printf 'COPY "%s" FROM STDIN WITH (FORMAT csv);\n' "$stg"
		# Every field is hex, decimal, or RFC 3339, so no CSV quoting; empty = NULL ('' exports as "x").
		sqlite_q "$db" "select $(join_by ', ' "${exports[@]}") from \"$t\"" || exit 1
		printf '\\.\n'
		printf 'INSERT INTO "%s" (%s) OVERRIDING SYSTEM VALUE SELECT %s FROM "%s";\n' \
			"$t" "$(quoted_columns "$db" "$t")" "$(join_by ', ' "${convert[@]}")" "$stg"
		printf "DO \$cutover\$ BEGIN IF (SELECT count(*) FROM \"%s\") <> %s THEN RAISE EXCEPTION 'cutover: %s row count differs from SQLite (%s)'; END IF; END \$cutover\$;\n" \
			"$t" "$expected" "$t" "$expected"
	done

	if [ "$db" = worker ]; then
		# Identity column: without this the next insert starts at 1 and hits 23505. AUTOINCREMENT
		# never reuses ids, so the floor is max(MAX(id), sqlite_sequence).
		local seq
		seq=$(sqlite_q "$db" "select coalesce((select seq from sqlite_sequence where name = 'job_attempts'), 0)") || exit 1
		printf "SELECT setval(pg_get_serial_sequence('job_attempts', 'id'), greatest(coalesce(max(id), 0), %s, 1), greatest(coalesce(max(id), 0), %s) > 0) FROM job_attempts;\n" "$seq" "$seq"
	fi

	local label sql_sqlite sql_pg value
	while IFS='|' read -r label sql_sqlite sql_pg; do
		[ -n "$label" ] || continue
		value=$(sqlite_q "$db" "$sql_sqlite") || exit 1
		printf "DO \$cutover\$ BEGIN IF (%s)::text <> '%s' THEN RAISE EXCEPTION 'cutover: %s differs from SQLite'; END IF; END \$cutover\$;\n" \
			"$sql_pg" "$value" "$label"
	done < <(aggregates "$db")
	printf 'COMMIT;\n'
}

aggregates() { if [ "$1" = api ]; then printf '%s\n' "$AGGREGATES_api"; else printf '%s\n' "$AGGREGATES_worker"; fi; }

load_db() {
	local db=$1
	printf '%s: loading in one transaction...\n' "$db"
	# errexit does not apply in this pipeline: on sqlite3 failure COMMIT is never printed, so it rolls back.
	local rc=0
	emit_load_sql "$db" | run_psql "$db" >/dev/null || rc=$?
	[ "$rc" = 0 ] || die "$db: load failed (exit $rc) and was rolled back; the target database is unchanged"
	printf '%s: committed\n' "$db"
}

FAILED=0
verify_db() {
	local db=$1 t s_count p_count s_digest p_digest status
	printf '\n%s verification (digest = SHA-256 of every row in key order)\n' "$db"
	printf '  %-36s %10s %10s  %-12s %s\n' table sqlite postgres digest result
	for t in $(spec_tables "$db" loaded); do
		s_count=$(sqlite_q "$db" "select count(*) from \"$t\"")
		p_count=$(psql_q "$db" "select count(*) from \"$t\"")
		s_digest=$(digest_sqlite "$db" "$t")
		p_digest=$(digest_pg "$db" "$t")
		status=ok
		if [ "$s_count" != "$p_count" ] || [ "$s_digest" != "$p_digest" ]; then
			status=MISMATCH
			FAILED=1
		fi
		printf '  %-36s %10s %10s  %-12s %s\n' "$t" "$s_count" "$p_count" "${s_digest:0:12}" "$status"
	done
	for t in $(spec_tables "$db" legacy); do
		printf '  %-36s %10s %10s  %-12s %s\n' "$t" "$(sqlite_q "$db" "select count(*) from \"$t\"")" - - 'skipped (legacy, unread)'
	done

	local label sql_sqlite sql_pg s_value p_value
	while IFS='|' read -r label sql_sqlite sql_pg; do
		[ -n "$label" ] || continue
		s_value=$(sqlite_q "$db" "$sql_sqlite")
		p_value=$(psql_q "$db" "$sql_pg")
		status=ok
		if [ "$s_value" != "$p_value" ]; then status=MISMATCH; FAILED=1; fi
		printf '  %-36s %10s %10s  %-12s %s\n' "$label" "$s_value" "$p_value" '' "$status"
	done < <(aggregates "$db")

	if [ "$db" = worker ]; then
		local floor next
		floor=$(sqlite_q "$db" "select max(coalesce((select max(id) from job_attempts), 0), coalesce((select seq from sqlite_sequence where name = 'job_attempts'), 0))")
		next=$(psql_q "$db" "select coalesce(pg_sequence_last_value(pg_get_serial_sequence('job_attempts', 'id')::regclass) + 1, 1)")
		status=ok
		if ! [ "$next" -gt "$floor" ] 2>/dev/null; then status=MISMATCH; FAILED=1; fi
		printf '  %-36s %10s %10s  %-12s %s\n' 'job_attempts next id > used ids' "$floor" "$next" '' "$status"
	fi
}

for db in $DATABASES; do check_sqlite "$db"; done
if [ "$MODE" = check ]; then
	for db in $DATABASES; do check_target "$db"; done
	printf '\nPre-flight checks passed; nothing was written.\n'
	exit 0
fi
if [ "$MODE" = all ]; then
	for db in $DATABASES; do check_target "$db"; done
	for db in $DATABASES; do load_db "$db"; done
fi
for db in $DATABASES; do verify_db "$db"; done

if [ "$FAILED" != 0 ]; then
	printf '\n' >&2
	die "Postgres does not match SQLite. Do NOT start flick-api: its reconciler would delete bucket objects of missing rows. Drop and recreate the target database(s), run the migrate subcommand, and load again."
fi
printf '\nOK: every table matches SQLite (%s).\n' "$DATABASES"
