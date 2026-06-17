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
			storage_backend text not null check (storage_backend in ('sqlite_blob', 'oci_object')),
			storage_key text not null,
			nonce text not null,
			kdf_algorithm text not null,
			kdf_salt text not null,
			kdf_params_json text not null,
			encrypted_filename text,
			content_type text,
			size_bytes integer not null check (size_bytes >= 0),
			max_views integer not null default 1 check (max_views > 0),
			view_count integer not null default 0 check (view_count >= 0),
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
	}

	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply api migration: %w", err)
		}
	}
	return nil
}
