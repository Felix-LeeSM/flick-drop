package db

import (
	"context"
	"database/sql"
	"fmt"
)

func MigrateAPI(ctx context.Context, conn *sql.DB) error {
	statements := []string{
		`create table if not exists secrets (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object')),
			storage_key text not null,
			nonce text not null,
			-- Model A secrets store KDF parameters so the browser can re-derive the
			-- passphrase key. Model B secrets encrypt with a random key placed in
			-- the URL fragment and carry no KDF, so these columns are nullable.
			kdf_algorithm text,
			kdf_salt text,
			kdf_params_json text,
			access_kdf_params_json text,
			access_proof_hash text,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
			failed_access_count integer not null default 0 check (failed_access_count >= 0),
			-- M3 large uploads land in S3 first; a secret starts as pending_upload
			-- and flips to active once /finalize confirms the object exists.
			state text not null default 'active' check (state in ('active', 'pending_upload')),
			expires_at datetime not null,
			consumed_at datetime,
			created_at datetime not null,
			updated_at datetime not null
		)`,
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
		`create table if not exists secret_payloads (
			secret_id text primary key,
			ciphertext blob not null,
			created_at datetime not null,
			foreign key (secret_id) references secrets(id) on delete cascade
		)`,
		`create table if not exists outbox_events (
			id text primary key,
			subject text not null,
			payload_json text not null,
			state text not null default 'pending'
				check (state in ('pending', 'published', 'failed')),
			attempts integer not null default 0 check (attempts >= 0),
			next_attempt_at datetime not null,
			published_at datetime,
			last_error text,
			created_at datetime not null,
			updated_at datetime not null
		)`,
		`create index if not exists idx_outbox_events_state_next_attempt
			on outbox_events(state, next_attempt_at)`,
	}

	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply api migration: %w", err)
		}
	}
	if err := ensureColumn(ctx, conn, "secrets", "access_kdf_params_json", "text"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "secrets", "access_proof_hash", "text"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "secrets", "failed_access_count", "integer not null default 0 check (failed_access_count >= 0)"); err != nil {
		return err
	}
	// Older deployments created `secrets` with NOT NULL kdf columns, without
	// the M3 `state` column, or with the legacy `oci_object` backend enum.
	// SQLite cannot drop a NOT NULL constraint or alter a CHECK via ALTER, so
	// such tables must be rebuilt to the current shape. No-op when the schema
	// is already normalized.
	if err := normalizeSecretsSchema(ctx, conn); err != nil {
		return err
	}
	// M4 reaper marks a row reclaimable (enqueued for hard-delete) the moment it
	// claims it, so concurrent reaper ticks/instances cannot double-enqueue.
	// Added after normalizeSecretsSchema: the rebuild path's column list omits
	// this column, so it must be (re)added once the table is at the current shape.
	if err := ensureColumn(ctx, conn, "secrets", "reclaim_enqueued_at", "datetime"); err != nil {
		return err
	}
	// Covers both reaper conditions (active expiry + pending_upload orphan) in a
	// single partial index over the rows still awaiting reclaim.
	if _, err := conn.ExecContext(ctx, `create index if not exists idx_secrets_reclaim_pending
			on secrets(state, expires_at, created_at)
			where reclaim_enqueued_at is null and consumed_at is null`); err != nil {
		return fmt.Errorf("create reclaim-pending index: %w", err)
	}
	return nil
}

// normalizeSecretsSchema rebuilds the secrets table to the current shape when an
// older database is detected: NOT NULL kdf columns (pre-Model-B), a missing
// `state` column (pre-M3), or the legacy `oci_object` backend enum. All rows
// are preserved. It is a no-op on databases already at the current schema.
// SQLite cannot drop NOT NULL or alter a CHECK in place, so the safe path is
// the table-rebuild described at https://sqlite.org/lang_altertable.html#otheralter.
func normalizeSecretsSchema(ctx context.Context, conn *sql.DB) error {
	// The `state` column is the M3 marker: a database that has it AND nullable
	// kdf columns is already normalized. Anything older (strict kdf, or no state
	// column) needs the rebuild.
	strictKDF, err := columnIsNotNull(ctx, conn, "secrets", "kdf_algorithm")
	if err != nil {
		return err
	}
	hasState, err := columnExists(ctx, conn, "secrets", "state")
	if err != nil {
		return err
	}
	if !strictKDF && hasState {
		return nil
	}

	// Rename the legacy backend enum before rebuild so copied rows satisfy the
	// new CHECK. Idempotent: a no-op when no `oci_object` rows exist.
	if _, err := conn.ExecContext(ctx, `update secrets set storage_backend='s3_object' where storage_backend='oci_object'`); err != nil {
		return fmt.Errorf("migrate storage_backend enum: %w", err)
	}

	// Column list must match the secrets table definition above (state excluded;
	// rebuilt rows take the 'active' default).
	const secretsColumns = `
			id, kind, storage_backend, storage_key, nonce, kdf_algorithm, kdf_salt,
			kdf_params_json, access_kdf_params_json, access_proof_hash,
			encrypted_filename, content_type, size_bytes, max_views,
			view_count, failed_access_count, expires_at, consumed_at, created_at, updated_at`

	// Disable FK enforcement outside the transaction; secret_payloads references
	// secrets, and rebuilding it must not cascade or error mid-migration.
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		return fmt.Errorf("disable foreign keys for secrets rebuild: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, `pragma foreign_keys=on`)
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin secrets rebuild tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rebuild := []string{
		`create table secrets_new (
				id text primary key,
				kind text not null check (kind in ('text', 'file')),
				storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object')),
				storage_key text not null,
				nonce text not null,
				kdf_algorithm text,
				kdf_salt text,
				kdf_params_json text,
				access_kdf_params_json text,
				access_proof_hash text,
				encrypted_filename text,
				content_type text,
				size_bytes integer not null check (size_bytes >= 0),
				max_views integer not null default 1 check (max_views > 0),
				view_count integer not null default 0 check (view_count >= 0),
				failed_access_count integer not null default 0 check (failed_access_count >= 0),
				state text not null default 'active' check (state in ('active', 'pending_upload')),
				expires_at datetime not null,
				consumed_at datetime,
				created_at datetime not null,
				updated_at datetime not null
			)`,
		`insert into secrets_new (` + secretsColumns + `) select ` + secretsColumns + ` from secrets`,
		`drop table secrets`,
		`alter table secrets_new rename to secrets`,
		`create index if not exists idx_secrets_expires_at on secrets(expires_at)`,
		`create index if not exists idx_secrets_consumed_at on secrets(consumed_at)`,
	}
	for _, stmt := range rebuild {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuild secrets table: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit secrets rebuild: %w", err)
	}
	return nil
}

// columnExists reports whether table has a column named column.
func columnExists(ctx context.Context, conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s column info: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s column info: %w", table, err)
	}
	return false, nil
}

func columnIsNotNull(ctx context.Context, conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s column info: %w", table, err)
		}
		if name == column {
			return notNull != 0, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s column info: %w", table, err)
	}
	return false, fmt.Errorf("column %s.%s not found", table, column)
}

func ensureColumn(ctx context.Context, conn *sql.DB, table, column, definition string) error {
	rows, err := conn.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan %s column info: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s column info: %w", table, err)
	}

	if _, err := conn.ExecContext(ctx, "alter table "+table+" add column "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s column: %w", table, column, err)
	}
	return nil
}

func MigrateWorker(ctx context.Context, conn *sql.DB) error {
	statements := []string{
		`create table if not exists job_receipts (
			job_id text primary key,
			kind text not null,
			state text not null
				check (state in ('processing', 'succeeded', 'failed', 'dead')),
			attempts integer not null default 0 check (attempts >= 0),
			last_error text,
			first_seen_at datetime not null,
			updated_at datetime not null,
			completed_at datetime
		)`,
		`create index if not exists idx_job_receipts_state_updated_at
			on job_receipts(state, updated_at)`,
		`create table if not exists job_attempts (
			id integer primary key autoincrement,
			job_id text not null,
			attempt integer not null,
			started_at datetime not null,
			finished_at datetime,
			result text not null check (result in ('running', 'succeeded', 'failed')),
			error text,
			foreign key (job_id) references job_receipts(job_id)
		)`,
		`create index if not exists idx_job_attempts_job_id on job_attempts(job_id)`,
		`create table if not exists dead_letters (
			job_id text primary key,
			kind text not null,
			payload_json text not null,
			error text not null,
			created_at datetime not null
		)`,
	}

	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply worker migration: %w", err)
		}
	}
	return nil
}
