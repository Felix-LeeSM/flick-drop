# Storage Backend Rename

This runbook moves an installation that used large-object storage before
`FLICK_STORAGE_BACKENDS` onto named backends. Releases before it stored the
fixed identifiers `s3_object` (the `FLICK_S3_*` backend) and `r2_object` (the
`FLICK_R2_*` backend) in the database; the current release stores the backend
name the operator chooses, such as `oci` or `r2`. It runs once, inside a planned
downtime of a few minutes.

You do not need this runbook when:

- the installation never enabled large-object storage
  (`FLICK_STORAGE_LARGE_BACKEND` was unset or `disabled`), or
- the installation is new.

In both cases flick-api migrates on its own: the legacy reconciliation cursor
rows are only scan positions, and the migration drops them
(`internal/db/storage_backends.go:retireLegacyStorageBackendChecks`).

## Why It Needs Downtime

flick-api refuses to start while any `secrets` or `requests` row, pending
reconciliation claim, or unpublished outbox event still names `s3_object` or
`r2_object`. The error names this runbook. The old release cannot read the new
names, and the new release cannot read the old ones. Every flick-api and
flick-worker replica therefore stops before the rename and starts on the new
release after it.

Swapping two names in the mapping cannot delete an object: object
reconciliation protects a bucket key whenever any object-backed row holds that
key (`internal/secrets/reconciliation.go:protectObjectTx`). A wrong mapping
makes opens of the affected objects fail until it is corrected, so check the
mapping before you run it.

## 1. Prepare the New Configuration

Replace the old variables with one entry per backend in
`FLICK_STORAGE_BACKENDS` (see `docs/architecture/env-contract.md`):

| Old variable | New variable for backend `oci` | New variable for backend `r2` |
| --- | --- | --- |
| `FLICK_STORAGE_LARGE_BACKEND=s3`, `FLICK_R2_ENABLED=true` | `FLICK_STORAGE_BACKENDS=oci,r2` | |
| `FLICK_S3_ENDPOINT` | `FLICK_STORAGE_OCI_ENDPOINT` | `FLICK_STORAGE_R2_ENDPOINT` (from `FLICK_R2_ENDPOINT`) |
| `FLICK_S3_REGION` | `FLICK_STORAGE_OCI_REGION` | `FLICK_STORAGE_R2_REGION` (`auto`) |
| `FLICK_S3_BUCKET` | `FLICK_STORAGE_OCI_BUCKET` | `FLICK_STORAGE_R2_BUCKET` |
| `FLICK_S3_ACCESS_KEY_ID` | `FLICK_STORAGE_OCI_ACCESS_KEY_ID` | `FLICK_STORAGE_R2_ACCESS_KEY_ID` |
| `FLICK_S3_SECRET_ACCESS_KEY` | `FLICK_STORAGE_OCI_SECRET_ACCESS_KEY` | `FLICK_STORAGE_R2_SECRET_ACCESS_KEY` |
| `FLICK_S3_PATH_STYLE` | `FLICK_STORAGE_OCI_PATH_STYLE` | `FLICK_STORAGE_R2_PATH_STYLE` |
| `FLICK_S3_MAX_TOTAL_BYTES` (default `18000000000`) | `FLICK_STORAGE_OCI_MAX_TOTAL_BYTES` | |
| `FLICK_R2_MAX_TOTAL_BYTES` (default `9000000000`) | | `FLICK_STORAGE_R2_MAX_TOTAL_BYTES` |

The old ceilings had built-in defaults; the new ones do not. With two or more
backends, set every `FLICK_STORAGE_<NAME>_MAX_TOTAL_BYTES` explicitly, or
flick-api refuses to start. To keep the old behaviour, use the old defaults.

`FLICK_CSP_CONNECT_SRC` keeps listing every backend endpoint origin.

Edit the one `insert` line in `docs/runbook/storage-backend-rename.sql` so each
legacy identifier maps to its new name. Remove the `r2_object` pair if R2 was
never enabled.

## 2. Stop flick-api and Let the Worker Drain

```sh
kubectl -n flick scale deploy/flick-api --replicas=0
kubectl -n flick rollout status deploy/flick-api
```

flick-api publishes the outbox, so nothing new reaches NATS once it is down.
Wait until the job stream is empty, which means the worker has finished every
delete that names an old identifier:

```sh
kubectl -n flick exec nats-0 -- wget -qO- 'http://127.0.0.1:8222/jsz?streams=true' \
  | grep -o '"messages": [0-9]*' | head -1
```

Continue when it prints `"messages": 0`, then stop the worker:

```sh
kubectl -n flick scale deploy/flick-worker --replicas=0
kubectl -n flick rollout status deploy/flick-worker
```

## 3. Rename the Identifiers

Take a backup first ([backup and restore](backup-restore.md)). Then run the SQL
file as the `postgres` superuser (or the role that owns the `flick_api` tables). It drops the old checks, renames the column
values, the pending claims, and the unpublished outbox payloads, and removes
the old cursor rows, all in one transaction:

```sh
kubectl -n flick exec -i deploy/postgres -- \
  psql -U postgres -d flick_api -v ON_ERROR_STOP=1 \
  < docs/runbook/storage-backend-rename.sql
```

Check that no legacy identifier remains:

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -Atc \
  "select storage_backend, count(*) from secrets group by 1 union all
   select storage_backend, count(*) from requests group by 1"
```

The output lists only `sqlite_blob` and your new names.

## 4. Start the New Release

Deploy the new images with the new configuration. Each flick-api replica
migrates on start, adds the backend-name checks, and reseeds one
reconciliation cursor per configured backend on its first pass.

Verify:

- `kubectl -n flick logs deploy/flick-api | grep 'object backend'` prints one
  `object backend <name> enabled` line per backend.
- `kubectl -n flick logs deploy/flick-worker | grep 'object deletion enabled'`
  prints one line per backend.
- A file upload above the inline threshold succeeds and opens.
- `flick_storage_bytes_used` shows one series per backend name.

## Rollback

Before step 3, scale both deployments back up on the old release.

After step 3, the old release cannot read the new names. Restore the backup
taken in step 3, then start the old release with the old configuration.
