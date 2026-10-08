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

create table secret_management (
  secret_id text primary key,
  token_hash blob not null check (length(token_hash) = 32),
  expires_at datetime not null,
  outcome text check (outcome in ('opened', 'locked', 'cancelled', 'unavailable'))
);

create index idx_secret_management_expires_at
  on secret_management(expires_at, secret_id);

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

## M8 Management Metadata

`secret_management` has no cascading foreign key: orphan/payload reclamation
must not erase an authenticated outcome before the original expiry. SHA-256
hashes are 32-byte BLOBs; raw management tokens are never persisted. Expiry uses
fixed-width UTC nanosecond timestamps for exact indexed comparisons. Existing
secrets receive no retroactive credential.

`internal/secrets/management.go` records `opened`/`locked` with the transition;
the reaper records orphan `unavailable` before deleting its secret row. Bounded
reaper sweeps purge expired management rows and their consumed secret metadata,
without changing legacy consumed-row retention. Public management reads enforce
the deadline even while cleanup is delayed. `cancelled` is reserved by the
[approved contract](../../contracts/sender-management-v1.md); #201 implements
cancellation and the separate object-reconciliation bookkeeping.
