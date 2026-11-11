# `internal/db/` Guide

This package owns SQLite setup, migrations, transactions, and low-level database
helpers.

Directory structure:

- Keep connection setup, migration execution, and transaction helpers separate
  by file.
- If embedded migrations become large enough for a `migrations/` directory, add
  a local `AGENTS.md` there before adding SQL files.
- Do not add domain-specific repositories here unless the repository boundary is
  explicitly documented.

Rules:

- Enable WAL, foreign keys, and busy timeout consistently for every SQLite
  connection.
- Preserve API-owned and worker-owned database separation.
- Do not store plaintext secret content, passphrases, derived keys, real
  credentials, or production identifiers in migrations or fixtures.

## Read-only domain aggregation

`storage_usage.go:StorageObjectBytes` is the one deliberate exception to the
"no domain repositories" rule: it aggregates `size_bytes` across the `secrets`
and `requests` tables per `storage_backend` (`s3_object`, `r2_object`) for the
per-backend placement ceilings enforced in `requests.Store.Reserve` and
`secrets.Store.CreateLarge` (see docs/architecture/storage-model.md). It only
reads; the secrets and requests stores remain the sole writers of their tables.
`RowQueryer` accepts `*sql.DB` or `*sql.Tx` because `Reserve` runs the
aggregate inside its open reservation transaction and the SQLite pool is a
single connection (`sqlite.go:SetMaxOpenConns(1)`) — an out-of-band read would
wait on that transaction's own connection and deadlock. Locked (consumed)
secrets keep counting for 35 minutes (the open-stream grace); opened secrets
stop counting at open because `secrets.Store.OpenTx` deletes their row and the
`secret_open_grace` record carries no size; consumed requests do not count;
uploading requests count twice because the aggregate cannot know whether
finalize has already copied the final object.

## Opened-secret purge

`migrations.go:forgetOpenedSecrets` runs inside `MigrateAPI` on every start. It
deletes `secrets` rows with `consumed_at is not null and view_count > 0`
(opened, not locked), their payloads by cascade, and `secret_management` rows
with `outcome = 'opened'`. An `s3_object`/`r2_object` row opened less than 35
minutes ago (`storage_usage.go:objectGrace`) first moves its `storage_key` and
deadline into `secret_open_grace`. After the first run it is a no-op, because
`secrets.Store.OpenTx` deletes those rows itself.
