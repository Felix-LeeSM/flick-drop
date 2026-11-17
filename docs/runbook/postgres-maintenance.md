# PostgreSQL Maintenance

This runbook covers routine PostgreSQL upkeep for Flick: autovacuum, dead-tuple
checks, manual `VACUUM` and `VACUUM FULL`, and disk-usage checks for the
`postgres-data` volume. It is public-safe and contains no real data.

Maintenance here is both an operational task (keep the 2Gi volume from filling)
and a **security-hygiene** task: a deleted row's ciphertext stays on disk as a
dead tuple until `VACUUM` reclaims it.

Related contracts:

- [Database schema](../architecture/database-schema.md)
- [Storage model](../architecture/storage-model.md)
- [Security model](../architecture/security-model.md#deletion-and-residual-risk)
- [Backup and restore](backup-restore.md)

## How Flick Uses PostgreSQL

- One `Deployment/postgres` (`postgres:17-alpine`, `deploy/base/postgres.yaml`)
  holds two logical databases on the `postgres-data` PVC (2Gi,
  `deploy/base/pvc.yaml`): `flick_api`, owned by the `flick_api` role, and
  `flick_worker`, owned by the `flick_worker` role.
- The base passes no PostgreSQL configuration, so the server runs with the
  image defaults, including autovacuum.
- Flick deletes rows constantly. An open deletes the secret's rows in the open
  transaction, the reaper hard-deletes reclaimable secret rows
  (`internal/secrets/reaper.go`), and `PurgePublishedTx` deletes published
  outbox rows after one hour (`internal/events/outbox.go`).
- A `DELETE` in PostgreSQL does not remove the row from disk. The old row
  version, including its `bytea` ciphertext in `secret_payloads` or
  `request_payloads`, stays in the table file as a **dead tuple** until
  `VACUUM` removes it. When a row grows past about 2 kB, PostgreSQL compresses
  its large values or moves them to the table's TOAST table, which `VACUUM`
  processes together with the table.

## Where To Run Commands

The `flick-api` and `flick-worker` images have no `psql` (`Dockerfile.api`,
`Dockerfile.worker`). Run everything inside the postgres pod, which connects as
the `postgres` superuser over the local socket:

```sh
kubectl -n flick exec -it deploy/postgres -- psql -U postgres -d flick_api
kubectl -n flick exec -it deploy/postgres -- psql -U postgres -d flick_worker
```

Run `VACUUM` as `postgres`, not as `flick_api` or `flick_worker`. A
database-wide `VACUUM` run by the owner role still works on Flick's tables but
prints `permission denied to vacuum "pg_authid", skipping it` and similar
warnings for the shared system catalogs.

## Autovacuum

Autovacuum is on in the image defaults. Check the values the running server
uses:

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -c \
  "SELECT name, setting, unit FROM pg_settings WHERE name IN ('autovacuum', 'autovacuum_naptime', 'autovacuum_vacuum_threshold', 'autovacuum_vacuum_scale_factor', 'min_wal_size', 'max_wal_size', 'archive_mode') ORDER BY name;"
```

The PostgreSQL 17 defaults are `autovacuum = on`, `autovacuum_naptime = 60s`,
`autovacuum_vacuum_threshold = 50`, and `autovacuum_vacuum_scale_factor = 0.2`.
Autovacuum vacuums a table once its dead tuples exceed 50 plus 20% of its live
rows.

That threshold matters on a quiet instance. A table with few live rows is not
vacuumed until about 50 rows have been deleted from it, so on a low-traffic
deployment the ciphertext of the last few opened or expired secrets can sit as
dead tuples for hours or days. Run a manual `VACUUM` when that matters to you.

## Check Dead Tuples

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c \
  "SELECT relname, n_live_tup, n_dead_tup, last_autovacuum, last_vacuum, pg_size_pretty(pg_total_relation_size(relid)) AS total_size FROM pg_stat_user_tables ORDER BY n_dead_tup DESC, relname;"
```

Repeat with `-d flick_worker`. `n_dead_tup` is the statistics collector's
estimate of dead tuples not yet removed; `last_autovacuum` and `last_vacuum`
show when each table was last cleaned. `total_size` includes indexes and the
TOAST table.

## Manual VACUUM

A plain `VACUUM` is safe while flick-api and flick-worker run. It takes a
`SHARE UPDATE EXCLUSIVE` lock, which does not block reads or writes:

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c "VACUUM (VERBOSE, ANALYZE);"
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_worker -c "VACUUM (VERBOSE, ANALYZE);"
```

`VACUUM` removes dead tuples and marks their space reusable inside the table
file, and gives empty pages at the end of the file back to the filesystem. It
does not shrink the file otherwise, and it does not promise to overwrite the
old bytes; new rows reuse the space over time.

A dead tuple that an open transaction can still see cannot be removed yet;
`VERBOSE` reports these as `dead but not yet removable`. Flick's own
transactions are short, so this normally clears on the next run.

## VACUUM FULL

`VACUUM FULL` writes a fresh copy of a table and its indexes, then deletes the
old files. Use it only when a table holds much more free space than live data,
for example after a large backlog was purged, and the volume needs that space
back.

Trade-offs on a small node:

- It takes an `ACCESS EXCLUSIVE` lock: every read and write of that table waits
  until it finishes. Creates and opens that touch the table stall for that
  time.
- It needs free disk for the new copy (roughly the table's live data plus its
  indexes) on the same 2Gi volume, plus WAL for the rewrite. Check
  [disk usage](#check-disk-usage) first.
- On a 1-vCPU node it competes with the API for CPU. Flick's tables are
  normally kilobytes to a few megabytes, so a rewrite takes seconds, but the
  lock still blocks requests for that time.

Take a [backup](backup-restore.md#back-up-the-databases) first and run it in a
maintenance window, one table at a time. `lock_timeout` makes it give up
instead of queueing behind a long transaction and blocking everything behind
it:

```sh
kubectl -n flick exec -i deploy/postgres -- psql -v ON_ERROR_STOP=1 -U postgres -d flick_api <<'SQL'
SET lock_timeout = '5s';
VACUUM (FULL, VERBOSE) secret_payloads;
SQL
```

## Check Disk Usage

```sh
# Volume
kubectl -n flick exec deploy/postgres -- df -h /var/lib/postgresql/data

# Each Flick database
kubectl -n flick exec deploy/postgres -- psql -U postgres -c \
  "SELECT datname, pg_size_pretty(pg_database_size(datname)) FROM pg_database WHERE datname LIKE 'flick_%';"

# Largest tables in one database (with indexes and TOAST)
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c \
  "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS total FROM pg_statio_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 10;"

# Write-ahead log
kubectl -n flick exec deploy/postgres -- psql -U postgres -c \
  "SELECT pg_size_pretty(sum(size)) AS wal_size, count(*) AS segments FROM pg_ls_waldir();"
```

The write-ahead log (WAL) shares the 2Gi volume with the data. With the
defaults (`min_wal_size = 80MB`, `max_wal_size = 1GB`), PostgreSQL keeps at
least 80 MB of WAL segments for reuse and lets WAL grow toward 1 GB between
checkpoints under heavy writes; `max_wal_size` is a soft limit. Leave that much
headroom on the volume.

If the volume fills, PostgreSQL can no longer write WAL and shuts down (a failed
WAL write is a `PANIC`), and flick-api and flick-worker fail with it. Free space or move to a larger volume
(through [backup and restore](backup-restore.md#volume-recreation-drill), or
PVC expansion if the cluster's StorageClass supports it) before that point.

## Residual Ciphertext

Deleting a secret means Flick no longer serves its ciphertext. It does not mean
every copy of the bytes is physically erased
([security model](../architecture/security-model.md#deletion-and-residual-risk)).
On this layout the copies are:

- **Dead tuples.** The deleted row's bytes stay in the table or TOAST file until
  `VACUUM` removes the tuple. After that the space is reusable but is not
  overwritten until new rows land in it.
- **Old table files after `VACUUM FULL`.** The old files are deleted, but the
  filesystem and the block device do not promise to overwrite the freed blocks.
- **WAL segments.** Every insert writes the new row, ciphertext included, to
  the WAL. A segment stays on the volume until a checkpoint recycles it and new
  WAL overwrites it. `archive_mode` is `off` in the image defaults, so no WAL
  archive copies exist unless an operator enables archiving.
- **Backups.** `pg_dump` archives and volume snapshots keep every row that
  existed when they were taken, until the archive or snapshot itself is
  destroyed. Rotate and destroy them on a schedule
  ([backup and restore](backup-restore.md)).
- **Pre-cutover SQLite volumes.** An installation migrated through
  [PostgreSQL cutover](postgres-cutover.md) keeps the old SQLite files, with
  their own free pages and WAL, until the `flick-api-data` and
  `flick-worker-data` volumes are deleted.

Treat the `postgres-data` volume, its snapshots, and every archive as private
data with the same handling as production secrets.
