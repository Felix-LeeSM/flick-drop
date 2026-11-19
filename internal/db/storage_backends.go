package db

import (
	"context"
	"database/sql"
	"fmt"
)

// legacyStorageBackendRunbook is where an operator learns how to rename the
// fixed backend identifiers used before FLICK_STORAGE_BACKENDS.
const legacyStorageBackendRunbook = "docs/runbook/storage-backend-rename.md"

// storageBackendNameCheck is the SQL form of storage.ValidBackendName.
const storageBackendNameCheck = `'^[a-z][a-z0-9]{0,15}$'`

// namedStorageBackendChecks are the constraints a fresh schema creates inline
// (migrations.go, requests.go). An upgraded database gets them here after its
// legacy enum checks are dropped.
var namedStorageBackendChecks = []struct{ table, name, check string }{
	{"secrets", "secrets_storage_backend_name", `storage_backend = 'sqlite_blob' or storage_backend ~ ` + storageBackendNameCheck},
	{"requests", "requests_storage_backend_name", `storage_backend = 'sqlite_blob' or storage_backend ~ ` + storageBackendNameCheck},
	{"requests", "requests_uploading_object", `state != 'uploading' or (storage_backend <> 'sqlite_blob' and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)`},
	{"requests", "requests_submitted_object_keys", `state != 'submitted' or storage_backend = 'sqlite_blob' or (final_key is not null and ciphertext_sha256 is not null)`},
	{"object_reconciliation_cursor_v2", "object_reconciliation_cursor_v2_backend_name", `backend ~ ` + storageBackendNameCheck},
	{"request_reconciliation_cursor_v2", "request_reconciliation_cursor_v2_backend_name", `backend ~ ` + storageBackendNameCheck},
}

// retireLegacyStorageBackendChecks moves a database created before
// FLICK_STORAGE_BACKENDS onto backend-name checks. The old schema allowed only
// the fixed identifiers 's3_object' and 'r2_object'; rows, claims, and pending
// delete events still carrying them must be renamed to configured backend
// names first (legacyStorageBackendRunbook), so this refuses to run while any
// remain. Reconciliation cursors are only scan positions, so legacy cursor rows
// are dropped and reseeded under the new names. A fresh or already-upgraded
// database passes through as a no-op.
func retireLegacyStorageBackendChecks(ctx context.Context, conn *sql.Conn) error {
	var legacy bool
	err := conn.QueryRowContext(ctx, `select
		exists (select 1 from secrets where storage_backend in ('s3_object', 'r2_object'))
		or exists (select 1 from requests where storage_backend in ('s3_object', 'r2_object'))
		or exists (select 1 from object_reconciliation_pending_v2 where backend in ('s3_object', 'r2_object'))
		or exists (select 1 from request_reconciliation_pending_v2 where backend in ('s3_object', 'r2_object'))
		or exists (select 1 from outbox_events where state <> 'published'
			and (payload_json like '%"backend":"s3_object"%' or payload_json like '%"backend":"r2_object"%'))`).Scan(&legacy)
	if err != nil {
		return fmt.Errorf("check legacy storage backend names: %w", err)
	}
	if legacy {
		return fmt.Errorf("the database still names object backends 's3_object' or 'r2_object'; rename them to FLICK_STORAGE_BACKENDS names first (%s)", legacyStorageBackendRunbook)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin storage backend check migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`delete from object_reconciliation_cursor_v2 where backend in ('s3_object', 'r2_object')`,
		`delete from request_reconciliation_cursor_v2 where backend in ('s3_object', 'r2_object')`,
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

// dropLegacyStorageBackendChecks drops every check constraint that still
// enumerates 's3_object'. Postgres named those inline checks itself
// (secrets_storage_backend_check, requests_check1, ...), so they are found by
// definition rather than by name. docs/runbook/storage-backend-rename.md runs
// the same block before renaming rows.
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
