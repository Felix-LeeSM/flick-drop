# Backup And Restore

This runbook describes how to back up and restore Flick state on the
PostgreSQL + Valkey Kubernetes layout in `deploy/base/`. It is public-safe and
intentionally contains no real tenancy values, kubeconfig, bucket names, or
database contents.

Flick keeps deliberately little durable state. Both databases are logical
databases on one PostgreSQL instance (`Deployment/postgres` in
`deploy/base/postgres.yaml`), so a backup is two `pg_dump` archives plus,
optionally, the NATS volume.

An installation that still runs the SQLite layout migrates once through
[PostgreSQL cutover](postgres-cutover.md) (`scripts/ops/migrate-sqlite-to-postgres.sh`)
before this runbook applies.

Related contracts:

- [Storage model](../architecture/storage-model.md)
- [Database schema](../architecture/database-schema.md)
- [Deployment target](../architecture/deployment-target.md)
- [PostgreSQL maintenance](postgres-maintenance.md)
- [OCI Free Tier deployment](oci-free-tier.md)

## What State Exists

| State | Location | Volume | Recreatable? |
| --- | --- | --- | --- |
| API database | `flick_api` on `Service/postgres` | `postgres-data` (2Gi) | No — source of truth for secrets, requests, reconciliation claims, and the outbox |
| Worker database | `flick_worker` on `Service/postgres` | `postgres-data` (2Gi) | No — job receipts, attempts, dead letters (idempotency state) |
| JetStream store | `/data/jetstream` in `StatefulSet/nats` | `nats-data` (1Gi) | Partly — see [JetStream](#jetstream) |
| Rate-limit buckets, live-transfer rooms | `Deployment/valkey` memory | none | Yes — see [Valkey](#valkey) |
| Large ciphertext | Primary S3-compatible bucket (`FLICK_S3_BUCKET`) | n/a (object storage) | No — but holds only browser-encrypted bytes |
| Large ciphertext | Secondary R2 bucket (`FLICK_R2_BUCKET`), only when `FLICK_R2_ENABLED=true` | n/a (object storage) | No — but holds only browser-encrypted bytes |

`deploy/base/pvc.yaml` declares only `postgres-data` and `nats-data`.
`flick-api` and `flick-worker` own no volume.

## Backup Principles

- **Run the client inside the postgres pod.** The `flick-api` and
  `flick-worker` images are `alpine:3.22` with only the service binary
  (`Dockerfile.api`, `Dockerfile.worker`) and have no `psql` or `pg_dump`. The
  `postgres:17-alpine` image does, and its `pg_dump` always matches the server
  version. Inside the pod, `-U postgres` connects over the local socket without
  a password.
- **`pg_dump` is a hot backup.** It reads one consistent snapshot of a database
  while flick-api and flick-worker keep running, so taking a backup needs no
  downtime. The two databases are dumped separately, so each archive has its
  own snapshot moment. A worker archive slightly older or newer than the API
  archive is safe: worker receipts only prevent repeated side effects, and
  cleanup jobs are idempotent (`docs/architecture/storage-model.md`, "Cleanup
  jobs are idempotent").
- **Use the custom format (`-Fc`).** It is compressed, `pg_restore` can list and
  fully read it to check integrity, and it restores into a database with a
  different owner through `--no-owner --role=...`.
- **Never pass `-t` to `kubectl exec`.** A TTY rewrites line endings and
  corrupts the binary archive. Use no flag for `pg_dump` and `-i` (stdin only)
  for `pg_restore`.
- **Backups retain ciphertext.** A `flick_api` archive contains
  browser-encrypted ciphertext for every secret and request that existed at
  dump time, and outbox rows that name object keys. Treat every archive as
  private data and store it the same way as production secrets.

## Back Up The Databases

```sh
ts=$(date -u +%Y%m%dT%H%M%SZ)

kubectl -n flick exec deploy/postgres -- pg_dump -U postgres -Fc flick_api    > "flick_api-$ts.dump"
kubectl -n flick exec deploy/postgres -- pg_dump -U postgres -Fc flick_worker > "flick_worker-$ts.dump"
```

Then validate each archive before trusting it as a backup:

```sh
for f in "flick_api-$ts.dump" "flick_worker-$ts.dump"; do
  # 1. Table of contents: proves the header and object list are readable.
  kubectl -n flick exec -i deploy/postgres -- pg_restore --list < "$f" | grep -c 'TABLE DATA'
  # 2. Full read: decompresses every data block. Must exit 0.
  kubectl -n flick exec -i deploy/postgres -- pg_restore -f /dev/null < "$f" && echo "$f: full read ok"
done

# 3. Checksums, stored next to the archives and re-checked before a restore.
sha256sum "flick_api-$ts.dump" "flick_worker-$ts.dump" > "SHA256SUMS-$ts"
sha256sum -c "SHA256SUMS-$ts"
```

Step 2 is the check that matters. `pg_restore --list` reads only the table of
contents at the front of the archive, so it still exits 0 when the tail of the
file is missing; the full read in step 2 fails with
`could not read from input file: end of file` on the same truncated file.

## Restore The Databases

A restore replaces the whole `flick_api` and `flick_worker` databases with the
archive contents. Restore into the same Flick release that took the backup:
`pg_restore` does not run migrations, and this runbook does not cover restoring
across releases.

### Why flick-api stays down until the restore finishes

Every flick-api replica runs object reconciliation: it lists `managed/secrets/`
and `managed/requests/` in the bucket and enqueues a delete for each object that
no live database row protects (`protectObjectTx` in
`internal/secrets/reconciliation.go`, `reconcileBackend` in
`internal/requests/cleanup.go`; contract in
[storage model](../architecture/storage-model.md#m8-management-retention-and-late-upload-reconciliation)).

While `flick_api` is empty or half restored, every live bucket object looks
like an orphan. A single reconciliation tick would then queue deletes for the
bodies of secrets that the archive is about to bring back, and the worker would
carry them out. flick-api also runs its migration on every start
(`db.MigrateAPI` in `cmd/flick-api/main.go`), which would create empty tables
in the recreated database before the restore. So scale flick-api (all
replicas) and flick-worker to 0, and start them again only after both databases
are restored and checked.

### Procedure

```sh
# 0. Re-check the archive checksums.
sha256sum -c "SHA256SUMS-$ts"

# 1. Stop every writer. Record the current replica counts first.
kubectl -n flick get deploy flick-api flick-worker
kubectl -n flick scale deploy/flick-api deploy/flick-worker --replicas=0
kubectl -n flick get pods -l 'app.kubernetes.io/name in (flick-api,flick-worker)'   # expect: No resources found

# 2. Recreate each database empty, owned by its role, with the same database
#    privileges deploy/base/postgres.yaml (ConfigMap/postgres-init) sets.
#    DROP DATABASE fails with "is being accessed by other users" if any
#    replica is still connected; stop and find it instead of forcing the drop.
kubectl -n flick exec -i deploy/postgres -- psql -v ON_ERROR_STOP=1 -U postgres -d postgres <<'SQL'
DROP DATABASE flick_api;
CREATE DATABASE flick_api OWNER flick_api;
REVOKE ALL ON DATABASE flick_api FROM PUBLIC;
DROP DATABASE flick_worker;
CREATE DATABASE flick_worker OWNER flick_worker;
REVOKE ALL ON DATABASE flick_worker FROM PUBLIC;
SQL

# 3. Restore. --no-owner --role makes every restored table owned by the
#    service role, whatever role owned it in the source cluster.
#    --single-transaction rolls the whole database back on the first error.
kubectl -n flick exec -i deploy/postgres -- \
  pg_restore -U postgres --no-owner --role=flick_api --single-transaction -d flick_api < "flick_api-$ts.dump"
kubectl -n flick exec -i deploy/postgres -- \
  pg_restore -U postgres --no-owner --role=flick_worker --single-transaction -d flick_worker < "flick_worker-$ts.dump"

# 4. Check ownership and row counts before restarting anything.
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c \
  "SELECT tablename, tableowner FROM pg_tables WHERE schemaname = 'public' ORDER BY 1;"
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c \
  "SELECT (SELECT count(*) FROM secrets) AS secrets, (SELECT count(*) FROM requests) AS requests, (SELECT count(*) FROM outbox_events) AS outbox;"
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_worker -c \
  "SELECT state, count(*) FROM job_receipts GROUP BY state;"

# 5. Start the services again with the counts recorded in step 1
#    (deploy/base: flick-api 2, flick-worker 1).
kubectl -n flick scale deploy/flick-worker --replicas=1
kubectl -n flick scale deploy/flick-api --replicas=2
kubectl -n flick rollout status deploy/flick-worker --timeout=180s
kubectl -n flick rollout status deploy/flick-api --timeout=180s
```

Every `tableowner` in step 4 must be `flick_api` (or `flick_worker` for the
worker database). The `REVOKE` in step 2 matters because a single-database
`pg_dump` archive does not carry database-level privileges.

`pg_restore --clean --if-exists` into the existing databases also works, but it
drops only the objects listed in the archive, so a table created after the
backup survives the restore. Dropping and recreating the database leaves
nothing behind.

Then run the application smoke test: create a text secret, open it once, and
confirm a second open is blocked.

## JetStream

JetStream state is **recreatable** and does not require backup in normal
operation:

- The API calls `EnsureStream` on startup (`cmd/flick-api/main.go`,
  `internal/events/nats.go`), so the stream and subject are re-created on an
  empty `nats-data` volume.
- The `outbox_events` table in `flick_api` is the durable source only for jobs
  **not yet published**. The publisher claims rows in state `pending` or
  `failed` (`OutboxStore.ClaimDueTx` in `internal/events/outbox.go`). A row
  marked `published` is never re-published, and `PurgePublishedTx` deletes it
  once `PublishedRetention` (one hour) has passed.

So an empty `nats-data` loses every job that JetStream had accepted but the
worker had not yet consumed: the outbox no longer treats those jobs as due. For
object-delete jobs the ciphertext object stays in the bucket until the API's
managed-object reconciliation (`ReconcileOnce` in
`internal/secrets/reconciliation.go` and `internal/requests/cleanup.go`) lists
it again and enqueues a new delete, or until bucket lifecycle rules remove it
(see [S3 Objects](#s3-objects)). Within the one-hour retention a lost job can
also be re-published by hand from its outbox row; see
[Worker cleanup recovery](worker-recovery.md).

To keep in-flight jobs across a broker loss, take a cold copy of `nats-data`.
NATS writes its store as uid 1000 (`deploy/base/nats.yaml`), so the maintenance
pod runs as the same user:

```sh
kubectl -n flick scale statefulset/nats --replicas=0
kubectl -n flick run nats-maint --restart=Never --image=alpine:3.22 \
  --overrides='{"spec":{"securityContext":{"runAsUser":1000,"runAsGroup":1000,"fsGroup":1000},"containers":[{"name":"nats-maint","image":"alpine:3.22","command":["sleep","3600"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"nats-data"}}]}}'
kubectl -n flick wait --for=condition=Ready pod/nats-maint --timeout=120s
kubectl -n flick exec nats-maint -- tar -C /data -czf - jetstream > "nats-data-$ts.tgz"
tar -tzf "nats-data-$ts.tgz" > /dev/null && echo "archive ok"
kubectl -n flick delete pod nats-maint
kubectl -n flick scale statefulset/nats --replicas=1
```

After a restore with an empty `nats-data`, expect the API to re-create the
stream and the outbox to re-publish only the rows still `pending` or `failed`.

## Valkey

Valkey has nothing to back up. `Deployment/valkey` runs with `--save ""` and
`--appendonly no` and mounts no volume (`deploy/base/valkey.yaml`); it holds
only the shared rate-limit buckets and live-transfer waiting rooms.

A Valkey restart empties it. While it is down, flick-api falls back to
per-replica rate limiting (`internal/httpapi/ratelimit.go`) and answers
`/api/pipe/*` with `503`. Live-transfer waiting rooms held in Valkey at that
moment are gone, so those transfers must be started again.

## S3 Objects

The primary bucket, and the secondary R2 bucket when `FLICK_R2_ENABLED=true`,
hold only browser-encrypted ciphertext for large files. Object lifecycle is
driven by the app, not by a backup tool:

- The reaper hard-deletes reclaimable secret rows and enqueues an object-delete
  job for bucket-backed rows (`internal/secrets/reaper.go`); the worker performs
  the delete (`internal/storage/object.go` `Delete`).
- Align bucket lifecycle rules with Flick TTL plus cleanup lag, as noted in
  [OCI Free Tier deployment](oci-free-tier.md).

Bucket backup is a provider-side concern (versioning / replication). If enabled,
account for the same residual-ciphertext property: versioned or replicated
objects keep ciphertext past logical deletion.

A database restore does not restore bucket objects. A restored secret whose
object was deleted after the backup was taken can no longer be opened; an object
uploaded after the backup has no row in the restored database, so
reconciliation deletes it as an orphan once flick-api starts.

## Volume Recreation Drill

`postgres-data` and `nats-data` are `ReadWriteOnce` PVCs. On a typical k3s
install they use the default `local-path` StorageClass, whose behavior is set
by the **cluster, not this repository** (the repository defines no StorageClass
or reclaim policy). Two defaults matter:

- **Node pinning.** `local-path` binds a PVC to the node where its first
  consumer pod is scheduled; the data physically lives on that node's disk. A
  pod rescheduled to another node sees an empty volume.
- **`reclaimPolicy: Delete`.** Deleting the PVC (or the bound PV) deletes the
  underlying data. There is no undo.

To move PostgreSQL to another node or recover after a volume loss:

```sh
# 1. Take both pg_dump archives AND pass their full-read and checksum checks
#    (Back Up The Databases). The delete in step 3 is irreversible; never reach
#    it on the strength of an archive you have not verified.

# 2. Stop every writer, then PostgreSQL itself.
kubectl -n flick scale deploy/flick-api deploy/flick-worker --replicas=0
kubectl -n flick scale deploy/postgres --replicas=0

# 3. Recreate the volume. The first pod to mount it binds it to its node.
kubectl -n flick delete pvc postgres-data        # destroys current data — archives must already be verified
kubectl -n flick apply -f deploy/base/pvc.yaml   # or the overlay PVC

# 4. Start PostgreSQL. On an empty volume, ConfigMap/postgres-init creates the
#    flick_api and flick_worker roles and empty databases, with the passwords
#    currently in Secret/flick-db-api and Secret/flick-db-worker.
kubectl -n flick scale deploy/postgres --replicas=1
kubectl -n flick rollout status deploy/postgres --timeout=180s
```

Then run [Restore The Databases](#procedure) from step 2 (the `DROP DATABASE`
statements drop the empty databases that `postgres-init` just created). Keep
flick-api at 0 until step 4 of the restore passes.

Avoid PVC deletion as a routine operation; it is data destruction, not a reset.

## Residual Risk

- Archives and bucket versions retain ciphertext after logical deletion until
  the archive or version itself is destroyed. Inside the live database, deleted
  rows remain as dead tuples until `VACUUM` reclaims them; see
  [PostgreSQL maintenance](postgres-maintenance.md).
- A restore brings back every secret and request that existed at backup time,
  including ones that were opened, cancelled, or locked after the backup. An
  inline secret opened after the backup becomes openable again until it expires.
- A restore needs flick-api and flick-worker downtime for its whole duration.
  Taking a backup does not.

Document backup retention and storage location in the private ops repository for
each real deployment.
