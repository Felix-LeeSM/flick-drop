# Storage Backend Rename

This runbook moves an installation that used large-object storage before
`FLICK_STORAGE_BACKENDS` onto named backends. Releases before it stored the
fixed identifiers `s3_object` (the `FLICK_S3_*` backend) and `r2_object` (the
`FLICK_R2_*` backend) in the database; the current release stores the backend
name the operator chooses, such as `oci` or `r2`. It runs once, inside a planned
downtime of a few minutes.

Every installation upgrading from those releases removes the old variables
(steps 1 and 3): flick-api and flick-worker refuse to start while any of them is set
(`internal/config/config.go:legacyStorageEnv`), so a deployment that was not
migrated fails loudly instead of running with object storage silently off.
Steps 2 to 4 are needed only if the installation enabled large-object storage
(`FLICK_STORAGE_LARGE_BACKEND=s3`). Otherwise flick-api migrates on its own:
the legacy reconciliation cursor rows are only scan positions, and the
migration drops them
(`internal/db/storage_backends.go:retireLegacyStorageBackendChecks`).

## Why It Needs Downtime

flick-api refuses to start while any `secrets` or `requests` row or
unpublished outbox event still names `s3_object` or `r2_object`, or an
unpublished object delete has no `backend`. The error names this runbook. The
old release cannot read the new names, and the new release cannot read the old
ones. Every flick-api and flick-worker replica therefore stops before the
rename and starts on the new release after it.

Nothing needs to drain first. A delete job already in NATS that names an old
identifier is rejected by the new worker as invalid. Its object is not lost:
the rename drops every pending reconciliation claim under an old identifier,
so the next reconciliation pass re-finds the object under its new backend name
and queues a fresh delete. Other jobs, such as `delete_secret`, run normally
once the new release is up.

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

Remove every old variable from the deployment. A Kubernetes Secret keeps a key
that a later `kubectl apply` no longer lists under `stringData`, so delete the
keys explicitly once both deployments are stopped (step 3).

Edit the one `insert` line in `docs/runbook/storage-backend-rename.sql` so each
legacy identifier maps to its new name. Remove the `r2_object` pair if R2 was
never enabled.

## 2. Stop flick-api and flick-worker

```sh
kubectl -n flick scale deploy/flick-api deploy/flick-worker --replicas=0
kubectl -n flick rollout status deploy/flick-api
kubectl -n flick rollout status deploy/flick-worker
```

## 3. Rename the Identifiers and Remove the Old Variables

Take a backup first ([backup and restore](backup-restore.md)). Then run the SQL
file as the `postgres` superuser (or the role that owns the `flick_api`
tables). In one transaction it drops the old checks, renames the row values
and the unpublished outbox payloads, drops unpublished object deletes that have
no backend, and removes the old cursor and pending-claim rows:

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

Delete the old variables from the Secret and ConfigMap the pods read. A key
that is already absent is skipped; each removed key is printed:

```sh
for key in FLICK_STORAGE_LARGE_BACKEND \
  FLICK_S3_ENDPOINT FLICK_S3_REGION FLICK_S3_BUCKET FLICK_S3_ACCESS_KEY_ID \
  FLICK_S3_SECRET_ACCESS_KEY FLICK_S3_PATH_STYLE FLICK_S3_MAX_TOTAL_BYTES \
  FLICK_R2_ENABLED FLICK_R2_ENDPOINT FLICK_R2_REGION FLICK_R2_BUCKET \
  FLICK_R2_ACCESS_KEY_ID FLICK_R2_SECRET_ACCESS_KEY FLICK_R2_PATH_STYLE \
  FLICK_R2_MAX_TOTAL_BYTES; do
  for obj in secret/flick-secrets configmap/flick-config; do
    kubectl -n flick patch "$obj" --type=json \
      -p "[{\"op\":\"remove\",\"path\":\"/data/$key\"}]" 2>/dev/null \
      && echo "removed $key from $obj"
  done
done
```

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

Delete jobs that were still in NATS under an old identifier are rejected right
after the start: the worker logs them as `object delete names unconfigured
backend "s3_object"` and records a dead letter for each. That burst is
expected; reconciliation queues fresh deletes for their objects.

## Rollback

Before step 3, scale both deployments back up on the old release.

After step 3, the old release cannot read the new names. Restore the backup
taken in step 3, restore the old variables, then start the old release with
the old configuration.
