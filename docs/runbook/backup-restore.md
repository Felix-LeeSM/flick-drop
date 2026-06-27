# Backup And Restore

This runbook describes how to back up and restore Flick state. It is public-safe
and intentionally contains no real tenancy values, kubeconfig, bucket names, or
database contents.

Flick keeps deliberately little durable state, and most of it is recreatable.
Read [Backup Principles](#backup-principles) before running any command: the
runtime image has no `sqlite3` client, so a naive `cp` of a live WAL database is
**not** a consistent backup.

Related contracts:

- [Storage model](../architecture/storage-model.md)
- [Database schema](../architecture/database-schema.md)
- [Deployment target](../architecture/deployment-target.md)
- [SQLite maintenance](sqlite-maintenance.md)
- [OCI Free Tier deployment](oci-free-tier.md)

## What State Exists

| State | Location | PVC | Recreatable? |
| --- | --- | --- | --- |
| API database | `/data/api.db` (SQLite WAL) | `flick-api-data` | No — source of truth for secrets and the outbox |
| Worker database | `/data/worker.db` (SQLite WAL) | `flick-worker-data` | No — job receipts and idempotency state |
| JetStream store | `/data/jetstream` | `nats-data` | Yes — the API re-creates the stream and replays from its outbox |
| Large ciphertext | S3-compatible bucket | n/a (object storage) | No — but holds only browser-encrypted bytes |

The databases are small (kilobytes to low megabytes for typical one-time-secret
traffic), so cold backups take seconds.

## Backup Principles

- **WAL is the trap.** Both databases run in WAL mode (`internal/db/sqlite.go`).
  A live `cp api.db` misses pages still in `api.db-wal` and can copy a torn
  state. A consistent copy requires either a clean shutdown (the closing
  connection checkpoints the WAL into the main file) or the SQLite online-backup
  API.
- **No `sqlite3` in the app image.** The runtime image is `alpine:3.22` with
  only `ca-certificates` and `tzdata` added (`Dockerfile.api`), running as the
  non-root `flick` user. There is no `sqlite3` binary and the app user cannot
  `apk add` one. So the supported path is a **cold backup**: stop the writer,
  then attach a short-lived root maintenance pod that mounts the same PVC,
  checkpoints the WAL, integrity-checks the file, and copies it out with
  `kubectl cp` (binary-safe). The same pod pattern is used by
  [SQLite maintenance](sqlite-maintenance.md).
- **Backups retain ciphertext.** A backup of `api.db` or the bucket contains
  browser-encrypted ciphertext, including for secrets that were logically
  deleted but not yet vacuumed. Treat every backup as private data and store it
  the same way as production secrets.

## Cold Backup

Stop the writer, then take an authoritative copy from a maintenance pod. The pod
checkpoints the WAL itself, so the copy is consistent regardless of how cleanly
the app terminated, and `kubectl cp` transfers the file over a tar stream (no
stdout contamination, unlike piping `cat`).

```sh
# 1. Quiesce the API writer (single connection, SetMaxOpenConns(1)).
kubectl -n flick scale deploy/flick-api --replicas=0
kubectl -n flick rollout status deploy/flick-api --timeout=120s
kubectl -n flick get pods -l app.kubernetes.io/name=flick-api   # expect: no resources

# 2. Attach a maintenance pod to the PVC. local-path node-affinity schedules it
#    on the PV's node; the RWO mount is free because the app is scaled to 0.
kubectl -n flick run flick-maint --restart=Never --image=alpine:3.22 \
  --overrides='{"spec":{"containers":[{"name":"flick-maint","image":"alpine:3.22","command":["sleep","3600"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"flick-api-data"}}]}}'
kubectl -n flick wait --for=condition=Ready pod/flick-maint --timeout=120s

# 3. Checkpoint + integrity-check, then copy out. integrity_check must print "ok".
kubectl -n flick exec flick-maint -- sh -c \
  'apk add --no-cache sqlite >/dev/null && \
   sqlite3 /data/api.db "PRAGMA wal_checkpoint(TRUNCATE); PRAGMA integrity_check;"'
kubectl -n flick cp flick-maint:/data/api.db ./api.db.bak

# 4. Validate the local copy BEFORE trusting it as a backup.
test "$(head -c 15 api.db.bak)" = "SQLite format 3" && echo "magic ok"  # SQLite header (16th byte is NUL)
test -s api.db.bak && sha256sum api.db.bak                              # non-empty + checksum

# 5. Tear down and resume.
kubectl -n flick delete pod flick-maint
kubectl -n flick scale deploy/flick-api --replicas=1
kubectl -n flick rollout status deploy/flick-api --timeout=120s
```

Repeat for `flick-worker` / `flick-worker-data` / `/data/worker.db`.

Do not trust a backup until step 4 passes — `rollout status` returning only means
replicas reached 0, not that the last container flushed cleanly, which is exactly
why the maintenance pod re-checkpoints before copying.

## JetStream

JetStream state is **recreatable** and does not require backup in normal
operation:

- The API calls `EnsureStream` on startup (`cmd/flick-api/main.go`,
  `internal/events/nats.go`), so the stream and subject are re-created on an
  empty `nats-data` PVC.
- The durable source of truth for pending jobs is the **outbox table in
  `api.db`**, not JetStream; the outbox publisher re-delivers from there.

So a JetStream backup is best-effort. If you want one anyway, back up the
`nats-data` PVC the same cold way (scale `statefulset/nats` to 0 first). After a
restore with an empty `nats-data`, expect the API to re-create the stream and
the outbox to re-publish any unacknowledged jobs.

## S3 Objects

The bucket holds only browser-encrypted ciphertext for large files. Object
lifecycle is driven by the app, not by a backup tool:

- The reaper hard-deletes reclaimable secret rows and enqueues an object-delete
  job for S3-backed rows (`internal/secrets/reaper.go`); the worker performs the
  delete (`internal/storage/object.go` `Delete`).
- Align bucket lifecycle rules with Flick TTL plus cleanup lag, as noted in
  [OCI Free Tier deployment](oci-free-tier.md).

Bucket backup is a provider-side concern (versioning / replication). If enabled,
account for the same residual-ciphertext property: versioned or replicated
objects keep ciphertext past logical deletion.

## Restore

Restore is the cold backup in reverse, through the same maintenance pod. Match
the database to a compatible schema version first — a restore does not run
migrations forward or back. **Stale WAL/SHM sidecars must be removed**: pairing a
restored `api.db` with a leftover `api.db-wal` corrupts the database.

```sh
kubectl -n flick scale deploy/flick-api --replicas=0
kubectl -n flick rollout status deploy/flick-api --timeout=120s

kubectl -n flick run flick-maint --restart=Never --image=alpine:3.22 \
  --overrides='{"spec":{"containers":[{"name":"flick-maint","image":"alpine:3.22","command":["sleep","3600"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"flick-api-data"}}]}}'
kubectl -n flick wait --for=condition=Ready pod/flick-maint --timeout=120s

# Drop any stale WAL/SHM, then copy the backup in.
kubectl -n flick exec flick-maint -- rm -f /data/api.db-wal /data/api.db-shm
kubectl -n flick cp ./api.db.bak flick-maint:/data/api.db

# Verify what landed: integrity + checksum match the source.
kubectl -n flick exec flick-maint -- sh -c \
  'apk add --no-cache sqlite >/dev/null && sqlite3 /data/api.db "PRAGMA integrity_check;"'
kubectl -n flick exec flick-maint -- sha256sum /data/api.db
sha256sum ./api.db.bak    # the two checksums must match

kubectl -n flick delete pod flick-maint
kubectl -n flick scale deploy/flick-api --replicas=1
kubectl -n flick rollout status deploy/flick-api --timeout=120s
```

Then run the application smoke test: create a text secret, open it once, and
confirm a second open is blocked.

## PVC Recreation Drill

`flick-api-data`, `flick-worker-data`, and `nats-data` are `ReadWriteOnce` PVCs.
On a typical k3s install they use the default `local-path` StorageClass, whose
behavior is set by the **cluster, not this repository** (the repo defines no
StorageClass or reclaim policy). Two defaults matter:

- **Node pinning.** `local-path` binds a PVC to the node where its first
  consumer pod is scheduled; the data physically lives on that node's disk. A
  pod rescheduled to another node sees an empty volume.
- **`reclaimPolicy: Delete`.** Deleting the PVC (or the bound PV) deletes the
  underlying data. There is no undo.

To move state to a specific node or recover after a PV loss:

```sh
# 1. Take a cold backup AND pass its step-4 validation (magic header + non-empty
#    + checksum). The delete in step 3 is irreversible; never reach it on the
#    strength of a backup you have not verified.
# 2. Stop the consumer.
kubectl -n flick scale deploy/flick-api --replicas=0

# 3. Recreate PVC (and, if pinning to a chosen node, schedule the consumer
#    there). The first pod to mount it binds the PVC to its node.
kubectl -n flick delete pvc flick-api-data        # destroys current data — backup must already be verified
kubectl -n flick apply -f deploy/base/pvc.yaml     # or the overlay PVC

# 4. Start the consumer on the target node, then run the Restore procedure above
#    (which removes stale WAL/SHM and re-verifies integrity + checksum).
kubectl -n flick scale deploy/flick-api --replicas=1
```

Avoid PVC deletion as a routine operation; it is data destruction, not a reset.

## Residual Risk

- Backups and bucket versions retain ciphertext after logical deletion until the
  source is vacuumed or lifecycle-expired. See
  [SQLite maintenance](sqlite-maintenance.md).
- A restore reintroduces secrets that were deleted after the backup was taken,
  including consumed ones if their rows had not yet been reaped at backup time.
- Cold backup requires brief writer downtime. Schedule it like any maintenance
  window; the reaper and outbox catch up on resume.

Document backup retention and storage location in the private ops repository for
each real deployment.
