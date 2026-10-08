# Database Schema

Flick uses separate SQLite files for service ownership.

## Ownership

```text
api.db
  owner: flick-api

worker.db
  owner: flick-worker
```

The worker must not mutate `api.db` directly. It calls internal API endpoints for
API-owned mutations.

## `api.db`

```sql
create table secrets (
  id text primary key,
  kind text not null check (kind in ('text', 'file')),
  storage_backend text not null check (storage_backend in ('sqlite_blob', 's3_object')),
  storage_key text not null,
  nonce text not null,
  kdf_algorithm text not null,
  kdf_salt text not null,
  kdf_params_json text not null,
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
);

create index idx_secrets_expires_at on secrets(expires_at);
create index idx_secrets_consumed_at on secrets(consumed_at);

create table secret_payloads (
  secret_id text primary key,
  ciphertext blob not null,
  created_at datetime not null,
  foreign key (secret_id) references secrets(id) on delete cascade
);

create table audit_events (
  id integer primary key autoincrement,
  secret_id text,
  event_type text not null,
  ip_hash text,
  user_agent_hash text,
  created_at datetime not null
);

create index idx_audit_events_secret_id on audit_events(secret_id);
create index idx_audit_events_created_at on audit_events(created_at);

create table outbox_events (
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

create index idx_outbox_events_state_next_attempt
  on outbox_events(state, next_attempt_at);
```

`kdf_algorithm`, `kdf_salt`, and `kdf_params_json` are not secret values. They
store the browser-side derivation parameters required to reproduce the same
derived key from the user-entered passphrase.

`access_kdf_params_json` stores separate browser-side derivation parameters for
the one-time open proof. `access_proof_hash` stores a server-side hash of that
proof. The proof is not an encryption key and cannot directly decrypt the
payload.

## Planned request schema

M9 [request links](request-links.md) require an API-owned `requests` table and
an inline payload table separate from existing send secrets. The proposed
record holds the public key, independent token hashes, bounded attempt receipt,
envelope, state, storage references, and original deadline. #205 introduces the
migration and exact SQL; no request table is present in the live schema above.
The worker must not read or write request tables directly.

## `worker.db`

```sql
create table job_receipts (
  job_id text primary key,
  kind text not null,
  state text not null
    check (state in ('processing', 'succeeded', 'failed', 'dead')),
  attempts integer not null default 0 check (attempts >= 0),
  last_error text,
  first_seen_at datetime not null,
  updated_at datetime not null,
  completed_at datetime
);

create index idx_job_receipts_state_updated_at
  on job_receipts(state, updated_at);

create table job_attempts (
  id integer primary key autoincrement,
  job_id text not null,
  attempt integer not null,
  started_at datetime not null,
  finished_at datetime,
  result text not null check (result in ('running', 'succeeded', 'failed')),
  error text,
  foreign key (job_id) references job_receipts(job_id)
);

create index idx_job_attempts_job_id on job_attempts(job_id);

create table dead_letters (
  job_id text primary key,
  kind text not null,
  payload_json text not null,
  error text not null,
  created_at datetime not null
);
```

`job_receipts.updated_at` also starts the one-minute processing lease.
`job_attempts` rows with `result = 'failed'` and `error IS NULL` record an
interrupted attempt whose outcome is unknown, not a handler failure. The worker
counts only failed attempts with a non-NULL error toward its three-failure limit.
The final failed attempt, `job_receipts.state = 'dead'`, and `dead_letters` row
are committed atomically. Recovery also honors failed attempts already stored
by earlier workers, without a schema migration.
See [event contract](event-contract.md) for recovery and acknowledgement rules.

## SQLite Settings

Expected runtime settings:

```sql
pragma journal_mode = wal;
pragma foreign_keys = on;
pragma busy_timeout = 5000;
```

Vacuum/checkpoint policy belongs in the operations runbook because it affects
disk usage and residual ciphertext retention.

## Planned M8 Management Metadata

The schema displayed above predates sender management.
[Sender management v1](../../contracts/sender-management-v1.md) proposes an
API-owned `secret_management` record with `secret_id`, `token_hash`, the original
`expires_at`, and nullable terminal `outcome`. The record survives early payload
cleanup but is purged at expiry, including for consumed secrets. Raw tokens and
recipient keys never enter the schema. Object-reconciliation cursor/pending-job
bookkeeping is operational cleanup state, separate from expiring management
authority. #200/#201 must add migrations and update this schema with runtime
implementation; this documentation change does not create tables.
