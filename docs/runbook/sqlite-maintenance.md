# SQLite Maintenance

This runbook covers WAL checkpointing and `VACUUM` for the Flick SQLite
databases (`api.db`, `worker.db`). It is public-safe and contains no real data.

Maintenance here is both an operational task (bound file growth) and a
**security-hygiene** task: logical deletes leave old ciphertext in freelist and
WAL pages until a checkpoint or vacuum reclaims them.

Related contracts:

- [Database schema](../architecture/database-schema.md)
- [Storage model](../architecture/storage-model.md)
- [Backup and restore](backup-restore.md)
- [Security model](../architecture/security-model.md)

## How Flick Uses SQLite

- WAL journaling, `busy_timeout=5000`, `foreign_keys=on`, and a single open
  connection (`SetMaxOpenConns(1)`) per process (`internal/db/sqlite.go`).
- The reaper hard-deletes reclaimable secret rows in a transaction
  (`internal/secrets/reaper.go`), so logical row growth is bounded automatically.
- But `DELETE` in SQLite does **not** shrink the file. Freed pages go on the
  freelist for reuse, and the WAL accumulates frames until a checkpoint. So even
  with the reaper running, the on-disk file holds residual pages — including old
  ciphertext — until maintenance reclaims them.

SQLite auto-checkpoints the WAL (default threshold ~1000 pages) and checkpoints
on a clean shutdown, so a healthy, restarting deployment rarely needs manual
intervention. Run manual maintenance when the WAL or main file grows unexpectedly
or as a deliberate security-hygiene step.

## Constraint: No `sqlite3` In The App Image

The runtime image is `alpine:3.22` (`Dockerfile.api`) with no `sqlite3` client
and a non-root user that cannot `apk add` one. Manual maintenance therefore runs
from a **separate maintenance pod** that mounts the same PVC, with the app
writer stopped so the single-writer invariant holds.

Always [back up](backup-restore.md) before maintenance, and run inside a
maintenance window — `VACUUM` rewrites the whole file and is slow on a 1-vCPU
node.

## Maintenance Pod

Stop the writer, then attach a throwaway pod with `sqlite` installed to the PVC:

```sh
kubectl -n flick scale deploy/flick-api --replicas=0
kubectl -n flick rollout status deploy/flick-api --timeout=120s

kubectl -n flick run flick-sqlite --rm -it --restart=Never \
  --image=alpine:3.22 \
  --overrides='{"spec":{"containers":[{"name":"flick-sqlite","image":"alpine:3.22","command":["sh"],"stdin":true,"tty":true,"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"flick-api-data"}}]}}'

# Inside the pod:
apk add --no-cache sqlite
```

Repeat for `flick-worker` / `flick-worker-data` / `/data/worker.db`.

## Checkpoint The WAL

Flush WAL frames into the main database and truncate the WAL file:

```sh
sqlite3 /data/api.db 'PRAGMA wal_checkpoint(TRUNCATE);'
```

`wal_checkpoint(TRUNCATE)` blocks new writers until it completes, which is why
the app writer must be stopped. The result row `0 | N | N` means a full
checkpoint succeeded. A clean shutdown of the app does the same thing, so this is
mainly for the rare case where the WAL grew while the app was running.

## Vacuum

`VACUUM` rebuilds the database into a fresh file with no freelist pages,
reclaiming space and overwriting residual ciphertext:

```sh
# Confirm there is something to reclaim.
sqlite3 /data/api.db 'PRAGMA freelist_count; PRAGMA page_count;'

# Rebuild in place (needs free disk roughly equal to the db size).
sqlite3 /data/api.db 'VACUUM;'

# Or rebuild to a new file (useful when disk is tight or for an offline copy).
sqlite3 /data/api.db "VACUUM INTO '/data/api.vacuumed.db';"
```

Notes:

- `VACUUM` requires temporary free space about the size of the database. On a
  near-full 1Gi PVC, prefer `VACUUM INTO` to a location with room, verify, then
  swap the file in while the writer is still stopped.
- Restart the writer after maintenance:

  ```sh
  kubectl -n flick scale deploy/flick-api --replicas=1
  kubectl -n flick rollout status deploy/flick-api --timeout=120s
  ```

## Optional: Auto-Vacuum

For deployments where deletes dominate, `auto_vacuum` keeps the freelist small
without a full `VACUUM`, but it must be set **before** tables are created (or
followed by one `VACUUM` to take effect) and adds per-commit work:

```sh
sqlite3 /data/api.db 'PRAGMA auto_vacuum;'            # 0 = none (default)
sqlite3 /data/api.db 'PRAGMA auto_vacuum=INCREMENTAL; VACUUM;'
# Later, reclaim a bounded number of freed pages without a full rewrite:
sqlite3 /data/api.db 'PRAGMA incremental_vacuum;'
```

This is optional; the default (manual `VACUUM` during a window) is sufficient for
typical one-time-secret traffic. Treat `auto_vacuum` as a tuning knob, not a
requirement.

## Cautions

- Never run `wal_checkpoint` or `VACUUM` against a database with a live writer —
  always scale the owning Deployment to 0 first.
- Always back up first ([Backup and restore](backup-restore.md)). A `VACUUM`
  that runs out of disk can leave a partial file.
- The vacuumed-away ciphertext is gone from the SQLite file, but any prior
  **backup** still contains it. Rotate or re-secure backups accordingly.
