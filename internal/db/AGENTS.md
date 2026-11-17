# `internal/db/` Guide

This package owns database connection setup, migrations, and low-level
database helpers. Both stores run on PostgreSQL since the M15 cutover:
`postgres.go:OpenPostgres` opens the pgx/v5/stdlib pool (`CGO_ENABLED=0`, no
SQLite driver in the binary), `migrations.go:MigrateAPI` owns the `flick_api`
schema, and `migrations_worker.go:MigrateWorker` owns the `flick_worker`
schema. Each migration takes a session-level `pg_advisory_lock` on a pinned
connection (`pg_advisory_lock($1)` + `pg_advisory_unlock` on the same
`*sql.Conn`) so concurrent replica boots cannot race DDL; the two services use
different lock keys (`migrations.go:apiMigrateLockKey`,
`migrations_worker.go:workerMigrateLockKey`).

Directory structure:

- Keep connection setup, migration execution, and transaction helpers separate
  by file.
- If embedded migrations become large enough for a `migrations/` directory, add
  a local `AGENTS.md` there before adding SQL files.
- Do not add domain-specific repositories here unless the repository boundary is
  explicitly documented.

Rules:

- Preserve API-owned (`flick_api`) and worker-owned (`flick_worker`) database
  separation: each service connects with its own least-privilege role.
- Every statement in `MigrateAPI` and `MigrateWorker` must stay idempotent:
  both run on every service start, including simultaneous starts of multiple
  replicas behind the advisory lock.
- Write Postgres dialect SQL only: `$n` placeholders, native `timestamptz`
  comparisons, `bytea` payloads, and `FOR UPDATE SKIP LOCKED` for multi-replica
  claims. No SQLite constructs (`?` binding, `datetime()`/`julianday()`,
  `zeroblob`, `insert or ignore`, `LastInsertId`).
- Do not store plaintext secret content, passphrases, derived keys, real
  credentials, or production identifiers in migrations or fixtures.

## Read-only domain aggregation

`storage_usage.go:StorageObjectBytes` is the one deliberate exception to the
"no domain repositories" rule: it aggregates `size_bytes` across the `secrets`
and `requests` tables per `storage_backend` (`s3_object`, `r2_object`) for the
per-backend placement ceilings enforced in `requests.Store.Reserve` and
`secrets.Store.CreateLarge` (see docs/architecture/storage-model.md). It only
reads; the secrets and requests stores remain the sole writers of their tables.
`RowQueryer` accepts `*sql.DB` or `*sql.Tx` so `Reserve` can run the aggregate
inside its open reservation transaction — the ceiling check and the claim write
then see the same transaction snapshot.

## Legacy-schema boundary

`MigrateAPI` deliberately contains no SQLite-era repair passes (AES-GCM/RSA
table drops, CHECK-widening rebuilds, opened-secret backlog purges): a
Postgres database reaches it either fresh or through the SQLite cutover
runbook, which migrates a current-release database that already carries this
schema. Do not add repair passes here; a genuinely new schema change gets a
new idempotent step instead.
