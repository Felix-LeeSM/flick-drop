CREATE TABLE secrets (
			id text primary key,
			kind text not null check (kind in ('text', 'file')),
			storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			storage_key text not null,
			access_kdf_params_json text,
			access_proof_hash text,
			wrapped_identity text,
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
			updated_at datetime not null,
			reclaim_enqueued_at datetime,
			check ((access_kdf_params_json is null) = (access_proof_hash is null)
				and (access_proof_hash is null) = (wrapped_identity is null))
		);
CREATE INDEX idx_secrets_expires_at on secrets(expires_at);
CREATE INDEX idx_secrets_consumed_at on secrets(consumed_at);
CREATE TABLE secret_payloads (
			secret_id text primary key,
			ciphertext blob not null,
			created_at datetime not null,
			foreign key (secret_id) references secrets(id) on delete cascade
		);
CREATE TABLE object_reconciliation_cursor (
            id integer primary key check (id = 1),
            continuation_token text not null default '',
            generation integer not null default 0
        );
CREATE TABLE object_reconciliation_pending (
            object_key text primary key,
            job_id text not null unique
        );
CREATE TABLE object_reconciliation_cursor_v2 (
			backend text primary key check (backend in ('s3_object', 'r2_object')),
			continuation_token text not null default '',
			generation integer not null default 0
		);
CREATE TABLE object_reconciliation_pending_v2 (
			backend text not null,
			object_key text not null,
			job_id text not null unique,
			primary key (backend, object_key)
		);
CREATE INDEX idx_secrets_storage_key on secrets(storage_key);
CREATE TABLE secret_management (
			secret_id text primary key,
			token_hash blob not null check (length(token_hash) = 32),
			expires_at datetime not null,
			outcome text check (outcome in ('opened', 'locked', 'cancelled', 'unavailable'))
		);
CREATE INDEX idx_secret_management_expires_at on secret_management(expires_at, secret_id);
CREATE TABLE secret_open_grace (
			object_key text primary key,
			grace_until datetime not null
		);
CREATE INDEX idx_secret_open_grace_until on secret_open_grace(grace_until);
CREATE TABLE usage_counters (
			day text not null,
			metric text not null,
			count integer not null default 0 check (count >= 0),
			primary key (day, metric)
		);
CREATE TABLE requests (
			id text primary key,
			public_key text not null,
			fingerprint text not null,
			encrypted_note text,
			submission_token_hash blob not null check (length(submission_token_hash) = 32),
			retrieval_token_hash blob not null check (length(retrieval_token_hash) = 32),
			state text not null default 'waiting' check (state in ('waiting', 'uploading', 'submitted', 'consumed', 'cancelled', 'unavailable')),
			generation integer not null default 1 check (generation between 1 and 16),
			attempt_token_hash blob check (length(attempt_token_hash) = 32),
			attempt_body_hash blob check (length(attempt_body_hash) = 32),
			kind text check (kind in ('text', 'file')),
			size_bytes integer check (size_bytes >= 0),
			envelope_json text,
			storage_backend text not null default 'sqlite_blob' check (storage_backend in ('sqlite_blob', 's3_object', 'r2_object')),
			upload_key text,
			final_key text,
			ciphertext_sha256 blob check (length(ciphertext_sha256) = 32),
			reservation_expires_at text,
			consumed_at text,
			expires_at text not null,
			check ((attempt_token_hash is null) = (attempt_body_hash is null)),
			check (state != 'uploading' or (storage_backend in ('s3_object', 'r2_object') and upload_key is not null and final_key is not null and ciphertext_sha256 is not null and reservation_expires_at is not null and kind = 'file' and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null)),
			check (state != 'submitted' or storage_backend not in ('s3_object', 'r2_object') or (final_key is not null and ciphertext_sha256 is not null)),
			check (state != 'submitted' or (kind is not null and size_bytes is not null and envelope_json is not null and attempt_token_hash is not null))
		);
CREATE INDEX idx_requests_expires_at on requests(expires_at, id);
CREATE TABLE request_payloads (
			request_id text primary key,
			ciphertext blob not null,
			foreign key (request_id) references requests(id) on delete cascade
		);
CREATE TABLE outbox_events (
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
		);
CREATE INDEX idx_outbox_events_state_next_attempt
			on outbox_events(state, next_attempt_at);
CREATE INDEX idx_secrets_reclaim_pending
			on secrets(state, expires_at, created_at)
			where reclaim_enqueued_at is null and consumed_at is null;
CREATE INDEX idx_requests_reservations on requests(reservation_expires_at, id) where state = 'uploading';
CREATE UNIQUE INDEX idx_requests_upload_key on requests(upload_key) where upload_key is not null;
CREATE UNIQUE INDEX idx_requests_final_key on requests(final_key) where final_key is not null;
CREATE TABLE request_reconciliation_cursor (id integer primary key check (id = 1), continuation_token text not null default '', generation integer not null default 0);
CREATE TABLE request_reconciliation_pending (object_key text primary key, job_id text not null unique);
CREATE TABLE request_reconciliation_cursor_v2 (
			backend text primary key check (backend in ('s3_object', 'r2_object')),
			continuation_token text not null default '',
			generation integer not null default 0
		);
CREATE TABLE request_reconciliation_pending_v2 (
			backend text not null,
			object_key text not null,
			job_id text not null unique,
			primary key (backend, object_key)
		);
