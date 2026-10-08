package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"
)

const requestsTableSchema = `create table if not exists requests (
			id text primary key,
			public_key text not null,
			fingerprint text not null,
			submission_token_hash blob not null check (length(submission_token_hash) = 32),
			retrieval_token_hash blob not null check (length(retrieval_token_hash) = 32),
			state text not null default 'waiting' check (state in ('waiting', 'uploading', 'submitted', 'consumed', 'cancelled', 'unavailable')),
			generation integer not null default 1 check (generation between 1 and 16),
			attempt_token_hash blob check (length(attempt_token_hash) = 32),
			attempt_body_hash blob check (length(attempt_body_hash) = 32),
			kind text check (kind in ('text', 'file')),
			size_bytes integer check (size_bytes >= 0),
			envelope_json text,
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 blob check (length(ciphertext_sha256) = 32),
			reservation_expires_at text,
			expires_at text not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend = 's3_object' and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend != 's3_object' or (final_key is not null and ciphertext_sha256 is not null)),
			check (state != 'submitted' or (kind is not null and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null))
		)`

// Existing inline databases need a table rebuild: SQLite cannot alter the state
// CHECK constraint. Pin the physical connection while foreign keys are disabled
// and preserve the separately owned inline payload table throughout the rebuild.
func normalizeRequestsSchema(ctx context.Context, db *sql.DB) (err error) {
	hasReservation, err := columnExists(ctx, db, "requests", "reservation_expires_at")
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if !hasReservation {
		if _, err = conn.ExecContext(ctx, "pragma foreign_keys=off"); err != nil {
			return err
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, restoreErr := conn.ExecContext(restoreCtx, "pragma foreign_keys=on"); restoreErr != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				err = errors.Join(err, restoreErr)
			}
		}()
		tx, beginErr := conn.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback()
		const columns = `id, public_key, fingerprint, submission_token_hash, retrieval_token_hash, state, generation, attempt_token_hash, attempt_body_hash, kind, size_bytes, envelope_json, expires_at`
		statements := []string{
			strings.Replace(requestsTableSchema, "if not exists requests (", "requests_new (", 1),
			"insert into requests_new (" + columns + ") select " + columns + " from requests",
			"drop table requests", "alter table requests_new rename to requests",
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("rebuild requests: %w", err)
			}
		}
		rows, checkErr := tx.QueryContext(ctx, "pragma foreign_key_check")
		if checkErr != nil {
			return checkErr
		}
		invalid := rows.Next()
		checkErr = rows.Err()
		_ = rows.Close()
		if checkErr != nil {
			return checkErr
		}
		if invalid {
			return errors.New("request migration would violate foreign keys")
		}
		if err = tx.Commit(); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, rollbackErr := conn.ExecContext(rollbackCtx, "rollback"); rollbackErr != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
			return fmt.Errorf("commit request migration: %w", err)
		}
	}
	for _, statement := range []string{
		`create index if not exists idx_requests_expires_at on requests(expires_at, id)`,
		`create index if not exists idx_requests_reservations on requests(reservation_expires_at, id) where state = 'uploading'`,
		`create unique index if not exists idx_requests_upload_key on requests(upload_key) where upload_key is not null`,
		`create unique index if not exists idx_requests_final_key on requests(final_key) where final_key is not null`,
		`create table if not exists request_reconciliation_cursor (id integer primary key check (id = 1), continuation_token text not null default '', generation integer not null default 0)`,
		`insert or ignore into request_reconciliation_cursor (id) values (1)`,
		`create table if not exists request_reconciliation_pending (object_key text primary key, job_id text not null unique)`,
	} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate request storage: %w", err)
		}
	}
	return nil
}
