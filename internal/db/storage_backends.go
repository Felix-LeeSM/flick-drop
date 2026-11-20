package db

import (
	"context"
	"database/sql"
	"fmt"
)

const legacyStorageBackendRunbook = "docs/runbook/storage-backend-rename.md"

// SQL form of storage.ValidBackendName.
const storageBackendNameCheck = `'^[a-z][a-z0-9]{0,15}$'`

// A fresh schema creates these inline (migrations.go, requests.go).
var namedStorageBackendChecks = []struct{ table, name, check string }{
	{"secrets", "secrets_storage_backend_name", `storage_backend = 'sqlite_blob' or storage_backend ~ ` + storageBackendNameCheck},
	{"requests", "requests_storage_backend_name", `storage_backend = 'sqlite_blob' or storage_backend ~ ` + storageBackendNameCheck},
	{"requests", "requests_uploading_object", `state != 'uploading' or (storage_backend <> 'sqlite_blob' and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)`},
	{"requests", "requests_submitted_object_keys", `state != 'submitted' or storage_backend = 'sqlite_blob' or (final_key is not null and ciphertext_sha256 is not null)`},
	{"object_reconciliation_cursor_v2", "object_reconciliation_cursor_v2_backend_name", `backend ~ ` + storageBackendNameCheck},
	{"request_reconciliation_cursor_v2", "request_reconciliation_cursor_v2_backend_name", `backend ~ ` + storageBackendNameCheck},
}

// Refuses while rows or pending delete events still carry 's3_object'/'r2_object' or no
// backend; drops legacy cursor and claim rows, since reconciliation re-finds surviving orphans.
func retireLegacyStorageBackendChecks(ctx context.Context, conn *sql.Conn) error {
	var legacy bool
	err := conn.QueryRowContext(ctx, `select
		exists (select 1 from secrets where storage_backend in ('s3_object', 'r2_object'))
		or exists (select 1 from requests where storage_backend in ('s3_object', 'r2_object'))
		or exists (select 1 from outbox_events where state <> 'published'
			and (payload_json like '%"backend":"s3_object"%' or payload_json like '%"backend":"r2_object"%'
				or (payload_json like '%"kind":"delete_oci_object"%' and payload_json not like '%"backend":%')))`).Scan(&legacy)
	if err != nil {
		return fmt.Errorf("check legacy storage backend names: %w", err)
	}
	if legacy {
		return fmt.Errorf("the database still has object rows or delete events naming 's3_object'/'r2_object', or an object delete without a backend; rename them to FLICK_STORAGE_BACKENDS names first (%s)", legacyStorageBackendRunbook)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin storage backend check migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`delete from object_reconciliation_cursor_v2 where backend in ('s3_object', 'r2_object')`,
		`delete from request_reconciliation_cursor_v2 where backend in ('s3_object', 'r2_object')`,
		`delete from object_reconciliation_pending_v2 where backend in ('s3_object', 'r2_object')`,
		`delete from request_reconciliation_pending_v2 where backend in ('s3_object', 'r2_object')`,
		dropLegacyStorageBackendChecks,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("retire legacy storage backend checks: %w", err)
		}
	}
	for _, c := range namedStorageBackendChecks {
		var present bool
		if err := tx.QueryRowContext(ctx, `select exists (select 1 from pg_constraint where conrelid = $1::regclass and conname = $2)`, c.table, c.name).Scan(&present); err != nil {
			return fmt.Errorf("look up constraint %s: %w", c.name, err)
		}
		if present {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`alter table %s add constraint %s check (%s)`, c.table, c.name, c.check)); err != nil {
			return fmt.Errorf("add constraint %s: %w", c.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit storage backend check migration: %w", err)
	}
	return nil
}

// Postgres named the inline checks itself, so they are found by definition; the
// storage-backend-rename runbook runs the same block before renaming rows.
const dropLegacyStorageBackendChecks = `do $$
declare c record;
begin
	for c in
		select conrelid::regclass as tbl, conname
		from pg_constraint
		where contype = 'c'
			and conrelid in ('secrets'::regclass, 'requests'::regclass,
				'object_reconciliation_cursor_v2'::regclass, 'request_reconciliation_cursor_v2'::regclass)
			and pg_get_constraintdef(oid) like '%s3_object%'
	loop
		execute format('alter table %s drop constraint %I', c.tbl, c.conname);
	end loop;
end $$`
