# PostgreSQL Cutover

This runbook moves an existing installation from the SQLite layout (the
`flick-api-data` and `flick-worker-data` volumes holding `/data/api.db` and
`/data/worker.db`) to the PostgreSQL layout in `deploy/base/postgres.yaml`. It
runs once, inside a planned downtime of roughly 15 to 30 minutes.

A fresh installation does not need this runbook: apply the base or your overlay
directly ([k3s base](k3s-base.md), [OCI free tier](oci-free-tier.md)).

## The Rule That Protects Your Bucket

**Never start flick-api against a `flick_api` database that is empty or
partially loaded while the object bucket still holds live objects.**

Every flick-api replica runs object reconciliation. It lists the bucket and
queues a delete for each object that no database row protects
(`internal/secrets/reconciliation.go`, `internal/requests/cleanup.go`). Against
an empty or partial database, every live secret and request body looks like an
orphan, and the worker deletes them. The same reasoning is spelled out for
restores in [backup and restore](backup-restore.md#why-flick-api-stays-down-until-the-restore-finishes).

Every step below exists to keep that rule. flick-api stays at zero replicas
until `scripts/ops/migrate-sqlite-to-postgres.sh` has loaded every table and
printed `OK: every table matches SQLite`.

## What Moves

`scripts/ops/migrate-sqlite-to-postgres.sh` copies every row of these tables.
The table list and column types are checked against both schemas on every run.

| Target database | Tables |
| --- | --- |
| `flick_api` (12) | `secrets`, `secret_payloads`, `secret_management`, `secret_open_grace`, `requests`, `request_payloads`, `outbox_events`, `usage_counters`, `object_reconciliation_cursor_v2`, `object_reconciliation_pending_v2`, `request_reconciliation_cursor_v2`, `request_reconciliation_pending_v2` |
| `flick_worker` (3) | `job_receipts`, `job_attempts`, `dead_letters` |

Four legacy SQLite tables are not copied: `object_reconciliation_cursor`,
`object_reconciliation_pending`, `request_reconciliation_cursor`, and
`request_reconciliation_pending`. The last SQLite release created them but
never read them; the `_v2` tables replaced them. The script still checks that
they exist and prints their row counts.

Valkey and NATS hold nothing to copy. Valkey keeps only rate-limit buckets and
Live transfer rooms, which expire within minutes. The NATS volume is unchanged
by the cutover; pending outbox rows are copied, so flick-api republishes any
job that was not yet published.

How values change on the way:

| SQLite value | PostgreSQL value | Handling |
| --- | --- | --- |
| RFC 3339 UTC text such as `2026-01-02T03:04:05.123456789Z` | `timestamptz` | Fraction truncated to microseconds (never rounded up); any other text format stops the script |
| `BLOB` (ciphertext, token hashes) | `bytea` | Copied as hex; byte-exact, including `0x00` and non-UTF-8 bytes |
| `TEXT` | `text` | Copied as hex of the UTF-8 bytes; NULL and `''` stay distinct |
| `INTEGER` | `integer` / `bigint` | Exact; a value out of range stops the load |
| `job_attempts.id` (`AUTOINCREMENT`) | `bigint generated always as identity` | Ids kept; the sequence restarts above both `MAX(id)` and `sqlite_sequence`, so the next worker insert cannot hit a duplicate key (`23505`) |

There are no boolean columns in either schema.

## Preconditions

Check each item before the maintenance window.

**Release.** Production runs a SQLite-era release whose schema is the final
SQLite schema: flick-worker from before PR #429 and flick-api from before
PR #432. The script compares the files against
`scripts/ops/testdata/sqlite-api-schema.sql` and
`scripts/ops/testdata/sqlite-worker-schema.sql` (column names, declared types,
NOT NULL, and primary keys) and stops on any difference. If your flick-worker
already runs on PostgreSQL, there is no `worker.db` to move: pass
`--only api` to the script and skip the worker parts of steps 2, 3, and 5.

**New release.** You have the images of the PostgreSQL release, and both
binaries support the migrate-only mode (`flick-api migrate`,
`flick-worker migrate`). Your overlay sets real passwords in
`Secret/flick-db-admin`, `Secret/flick-db-api`, and `Secret/flick-db-worker`,
not `replace-me-before-use`.

**Automated deploys paused.** Nothing may re-apply your overlay while this
runbook runs: pause every deploy workflow, GitOps sync, or self-heal that
targets the `flick` namespace. Your overlay sets flick-api to two replicas, so
an automated apply between steps 4 and 9 starts flick-api against an empty or
partially loaded `flick_api` database, the exact situation the rule above
forbids. Turn them back on at the end of step 9.

**Operator machine.** `kubectl` with access to the `flick` namespace,
`sqlite3` 3.31 or newer, `bash`, `sha256sum` or `shasum`, and a checkout of the
new release (for the script and for the kustomization in step 4).

**Disk.** The operator machine has free space of at least three times the size
of `api.db` plus `worker.db` (copy, cold backup, and margin). The node that
will hold `postgres-data` has room for the same data again.

**Rehearsal (recommended).** On the new release checkout, with Docker and a
local `postgres:17` container, run the regression harness once:

```sh
docker run -d --rm --name flick-cutover-rehearsal -e POSTGRES_PASSWORD=rehearsal \
  -p 127.0.0.1:15499:5432 postgres:17-alpine
sleep 5
CUTOVER_TEST_CONTAINER=flick-cutover-rehearsal CUTOVER_TEST_PG_PORT=15499 \
  CUTOVER_TEST_PG_PASSWORD=rehearsal bash scripts/ops/test-migrate-sqlite-to-postgres.sh
docker stop flick-cutover-rehearsal
```

It builds both binaries, creates throwaway databases, loads fixture data, and
checks the refusals. It must end with `0 failed`.

**Record the current state** so that a rollback restores the same images and
replica counts:

```sh
kubectl -n flick get deploy -o wide > pre-cutover-deployments.txt
kubectl -n flick get pvc > pre-cutover-pvcs.txt
```

Keep the manifests (or overlay revision) of the running SQLite release at hand.
The rollback in this runbook re-applies them.

## Procedure

Run the steps in order, in one shell. The working directory holds ciphertext;
keep it private and destroy it after the retention period (see
[After The Cutover](#after-the-cutover)).

```sh
WORK="$HOME/flick-cutover-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$WORK" && chmod 700 "$WORK"
cd "$WORK"
```

### 1. Stop every writer

```sh
kubectl -n flick scale deploy/flick-api deploy/flick-worker --replicas=0
kubectl -n flick get pods -l 'app.kubernetes.io/name in (flick-api,flick-worker)'   # expect: No resources found
```

Wait until no pod is listed, including `Terminating` ones. From here on, the
SQLite files no longer change.

### 2. Copy the SQLite files out

The API and worker images have no `sqlite3` and their pods are stopped, so a
temporary busybox pod mounts each old volume **read-only** and streams the files out.
The pod sets no `fsGroup`, so Kubernetes does not change file ownership on the
volume either. The old volumes stay byte-for-byte unchanged, which is what
makes the rollback safe.

```sh
for svc in api worker; do
kubectl -n flick apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: flick-sqlite-copy-$svc
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 100
    runAsGroup: 100
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: copy
      image: busybox:1.37
      command: ["sleep", "3600"]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: data
          mountPath: /data
          readOnly: true
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: flick-$svc-data
        readOnly: true
EOF
done
kubectl -n flick wait --for=condition=Ready pod/flick-sqlite-copy-api pod/flick-sqlite-copy-worker --timeout=180s
```

Copy each database file together with its `-wal` and `-shm` files, if
present, and compare checksums taken inside the pod with the local copies:

```sh
for svc in api worker; do
  mkdir -p "$WORK/$svc"
  kubectl -n flick exec flick-sqlite-copy-$svc -- ls -ln /data
  kubectl -n flick exec flick-sqlite-copy-$svc -- sh -c 'cd /data && tar cf - *.db*' | tar xf - -C "$WORK/$svc"
  kubectl -n flick exec flick-sqlite-copy-$svc -- sh -c 'cd /data && sha256sum *.db*' > "$WORK/$svc/SHA256SUMS.pvc"
  (cd "$WORK/$svc" && sha256sum -c SHA256SUMS.pvc)   # macOS: shasum -a 256 -c SHA256SUMS.pvc
done
kubectl -n flick delete pod flick-sqlite-copy-api flick-sqlite-copy-worker
```

Every line of the checksum check must end in `OK`.

### 3. Checkpoint the WAL and take a cold backup

Both databases run in WAL mode, so committed rows may still sit in the `-wal`
file. Fold them into the main file **on the local copy**, check integrity, and
write a cold backup with `.backup`:

```sh
for svc in api worker; do
  db="$WORK/$svc/$svc.db"
  sqlite3 "$db" 'PRAGMA wal_checkpoint(TRUNCATE);'   # expect: 0|0|0
  sqlite3 "$db" 'PRAGMA integrity_check;'            # expect: ok
  sqlite3 "$db" ".backup '$WORK/$svc-cold-backup.db'"
done
find "$WORK" -name '*.db-wal' -size +0 -print   # must print nothing (an empty or absent -wal is fine)
```

The checkpoint runs on the copy rather than on the volume because the old
images cannot run `sqlite3`, and because the volume must stay untouched for
the rollback. Opening the copy replays its `-wal` file first, so no committed
row is lost. Store the cold backups with the same care as production secrets.

### 4. Apply the new layout with the application held at zero

Applying the new overlay directly would start flick-api against an empty
database. Wrap the overlay in a kustomization that pins `flick-api`,
`flick-worker`, and `flick-web` to zero replicas. Create it as a sibling of
your overlay directory, because kustomize accepts only relative paths:

```sh
mkdir -p <overlay-parent>/cutover-hold
cat > <overlay-parent>/cutover-hold/kustomization.yaml <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../<overlay-name>
patches:
  - target:
      kind: Deployment
      name: flick-(api|worker|web)
    patch: |-
      - op: replace
        path: /spec/replicas
        value: 0
EOF
```

Check the rendered replica counts before applying. The three application
Deployments must show `0`:

```sh
kubectl kustomize <overlay-parent>/cutover-hold | awk '/^kind:/{k=$2} /^  name:/{n=$2} /^  replicas:/{print k, n, $2}'
```

Then apply it and wait for PostgreSQL and Valkey:

```sh
kubectl apply -k <overlay-parent>/cutover-hold
kubectl -n flick rollout status deploy/postgres --timeout=300s
kubectl -n flick rollout status deploy/valkey --timeout=180s
kubectl -n flick get deploy flick-api flick-worker flick-web   # every READY column: 0/0
```

flick-web stops here too, so the site is fully down until step 9.

`kubectl apply` does not delete objects missing from the new manifests, so the
`flick-api-data` and `flick-worker-data` volumes stay in place. On its first
start, PostgreSQL runs `ConfigMap/postgres-init`, which creates the `flick_api`
and `flick_worker` roles and databases.

### 5. Create the empty schemas with the migrate subcommands

`flick-worker migrate` and `flick-api migrate` apply the PostgreSQL schema
(`internal/db/migrations_worker.go:MigrateWorker`,
`internal/db/migrations.go:MigrateAPI`) and exit. They start no reconciler, no
NATS connection, and no listener. Run each once as a Job with the image of the
Deployment you just applied. The pod labels match `NetworkPolicy/postgres`,
which admits only flick-api and flick-worker pods. A network policy
controller (k3s kube-router, for one) admits a new pod a few seconds after it
starts, so the first connection can be refused; both binaries retry their
first ping for up to 30 seconds (`internal/db/postgres.go:OpenPostgres`).
`restartPolicy: OnFailure` with `backoffLimit: 3` covers a slower controller.
A failed Job does not satisfy the `kubectl wait` below; if the wait times out,
read the logs it prints next.

```sh
API_IMAGE=$(kubectl -n flick get deploy/flick-api -o jsonpath='{.spec.template.spec.containers[0].image}')
WORKER_IMAGE=$(kubectl -n flick get deploy/flick-worker -o jsonpath='{.spec.template.spec.containers[0].image}')

for svc in worker api; do
  if [ "$svc" = api ]; then
    image=$API_IMAGE; secret=flick-db-api; role=flick_api
    pw_var=FLICK_API_DB_PASSWORD; url_var=FLICK_DATABASE_URL
  else
    image=$WORKER_IMAGE; secret=flick-db-worker; role=flick_worker
    pw_var=FLICK_WORKER_DB_PASSWORD; url_var=FLICK_WORKER_DATABASE_URL
  fi
kubectl -n flick apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: flick-$svc-migrate
spec:
  backoffLimit: 3
  template:
    metadata:
      labels:
        app.kubernetes.io/name: flick-$svc
        app.kubernetes.io/part-of: flick
    spec:
      restartPolicy: OnFailure
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 100
        runAsGroup: 100
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: migrate
          image: $image
          args: ["migrate"]
          env:
            - name: $pw_var
              valueFrom:
                secretKeyRef:
                  name: $secret
                  key: password
            - name: $url_var
              value: "postgres://$role:\$($pw_var)@postgres:5432/$role?sslmode=disable"
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
EOF
  kubectl -n flick wait --for=condition=complete job/flick-$svc-migrate --timeout=180s
  kubectl -n flick logs job/flick-$svc-migrate
done
```

The logs must end with `flick-worker migrated the flick_worker database` and
`flick-api migrated the flick_api database`. Delete the Jobs afterwards:

```sh
kubectl -n flick delete job flick-worker-migrate flick-api-migrate
```

Because the Jobs connect as `flick_api` and `flick_worker`, those roles own
every table, exactly as after a normal service start.

### 6. Run the pre-flight checks

The script runs `psql` through the command prefix you pass; it appends
`-X -q -v ON_ERROR_STOP=1` and sends SQL on stdin. Through `kubectl exec`, psql
connects over the pod's local socket as `postgres`, so no password crosses the
command line.

```sh
SCRIPT=<release-checkout>/scripts/ops/migrate-sqlite-to-postgres.sh
API_PSQL='kubectl -n flick exec -i deploy/postgres -- psql -U postgres -d flick_api'
WORKER_PSQL='kubectl -n flick exec -i deploy/postgres -- psql -U postgres -d flick_worker'

bash "$SCRIPT" --check-only \
  --api-sqlite "$WORK/api/api.db" --api-psql "$API_PSQL" \
  --worker-sqlite "$WORK/worker/worker.db" --worker-psql "$WORKER_PSQL"
```

It must end with `Pre-flight checks passed; nothing was written.` It checks:

- each SQLite file: empty `-wal`, UTF-8 encoding, `PRAGMA integrity_check`,
  `PRAGMA foreign_key_check`, the exact table and column set of the final
  SQLite release, and the type and timestamp format of every stored value;
- each target: UTF-8 encoding, the exact table and column set of the HEAD
  schema, and no rows (the `*_cursor_v2` tables may hold only the two seed rows
  that `MigrateAPI` inserts).

A failure names the database, table, and column and changes nothing. Fix the
cause and run the check again; do not edit the script to get past it.

### 7. Load and verify

Run the same command without `--check-only`:

```sh
set -o pipefail   # so the pipe through tee keeps the script's exit status
bash "$SCRIPT" \
  --api-sqlite "$WORK/api/api.db" --api-psql "$API_PSQL" \
  --worker-sqlite "$WORK/worker/worker.db" --worker-psql "$WORKER_PSQL" \
  | tee "$WORK/cutover-report.txt"
echo "exit=$?"   # must print exit=0
```

For each database, the script runs one transaction: it locks the target tables,
re-checks that they are empty, replaces the two seed rows of each
`*_cursor_v2` table, copies every table, resets the `job_attempts` identity
sequence, and asserts row counts and the aggregate checks below before
`COMMIT`. Any error rolls that database back completely.

After both commits it verifies every table again from scratch and prints a
report. The output contains only counts, byte sums, and table-level digests,
never payloads or token hashes.

### Verification criteria

The run passes only when all of the following hold. The script checks each
one and exits non-zero otherwise.

- **Row counts** match for each of the 15 tables.
- **Row digests** match: for each table, the SHA-256 of every row in primary-key
  order, rendered independently by SQLite and by PostgreSQL in one canonical
  text form (NULL, decimal, hex of text and bytes, UTC timestamp to the
  microsecond). This covers every column of every row.
- **Aggregates** match: `secrets` `SUM(size_bytes)` and the count of
  `state = 'active'` rows, `secret_payloads` and `request_payloads` total
  ciphertext bytes, `requests` `SUM(size_bytes)`, `usage_counters`
  `SUM(count)`, and `job_attempts` `MAX(id)`.
- **Identity**: the next `job_attempts` id is greater than both `MAX(id)` and
  the SQLite `sqlite_sequence` value.

The last line must read:

```text
OK: every table matches SQLite (api worker).
```

If the load fails, the target is unchanged and you can run step 7 again after
fixing the cause. If the load committed but verification reports `MISMATCH`,
do **not** start flick-api. Drop and recreate only the database that failed,
repeat step 5 for it, and load it again with `--only api` or `--only worker`.
For `flick_api`:

```sh
kubectl -n flick exec -i deploy/postgres -- psql -v ON_ERROR_STOP=1 -U postgres -d postgres <<'SQL'
DROP DATABASE flick_api;
CREATE DATABASE flick_api OWNER flick_api;
REVOKE ALL ON DATABASE flick_api FROM PUBLIC;
SQL
```

For `flick_worker`, run the same three statements with `flick_worker` in
place of `flick_api`. To re-run only the verification later, add
`--verify-only`.

### 8. Refresh planner statistics

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -c 'ANALYZE;'
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_worker -c 'ANALYZE;'
```

### 9. Start the services in order

Before starting anything, note the time and the number of objects the loaded
rows protect. The post-checks compare against them.

```sh
START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -Atc \
  "SELECT (SELECT count(*) FROM secrets WHERE storage_backend <> 'sqlite_blob')
        + (SELECT count(*) FROM requests WHERE upload_key IS NOT NULL OR final_key IS NOT NULL);"
```

Start flick-api alone and keep flick-worker at zero:

```sh
kubectl -n flick scale deploy/flick-api --replicas=2
kubectl -n flick rollout status deploy/flick-api --timeout=300s
```

flick-api only queues object deletes; flick-worker carries them out. With the
worker stopped, every delete that reconciliation queues waits in the
`FLICK_JOBS` stream, so the tripwire below can still stop it. Each flick-api
replica reconciles once as soon as it starts and again every
`FLICK_REAPER_INTERVAL_SECONDS` (60 by default,
`internal/secrets/reaper.go:DefaultReaperInterval`).

**Reconciliation tripwire.** Wait one reaper interval after both replicas are
`Ready`, then count the orphan deletes queued since `START`:

```sh
kubectl -n flick exec deploy/postgres -- psql -U postgres -d flick_api -Atc \
  "SELECT count(*) FROM outbox_events WHERE created_at >= '$START' AND payload_json::jsonb->>'reason' = 'orphan';"
```

A handful is normal: objects left behind by earlier failures. A number close
to the object count noted above, or to the size of one bucket listing page
(`FLICK_REAPER_BATCH_SIZE` keys per backend, at most 1,000), means the
database does not match the bucket. In that case stop flick-api, wait until
its pods are gone (each one keeps reconciling and publishing during its
5-second `preStop`), and only then drop the queued deletes, before any worker
runs:

```sh
kubectl -n flick scale deploy/flick-api --replicas=0
kubectl -n flick wait --for=delete pod -l app.kubernetes.io/name=flick-api --timeout=120s &&
  kubectl -n flick run nats-purge --rm -i --restart=Never --image=natsio/nats-box:0.18.1 -- \
    nats -s nats://nats:4222 stream purge FLICK_JOBS -f
```

If the wait times out, the purge does not run: find the pod that is still
terminating and run both commands again once it is gone.

Then reload `flick_api` with the `flick_api` block from the `MISMATCH`
paragraph of step 7: drop and recreate only `flick_api`, repeat step 5 for
it, and run step 7 with `--only api`. The script refuses the current database because it is not
empty, and dropping it also removes the orphan deletes that are still
unpublished in `outbox_events`. `flick_worker` is untouched, because no worker
has run yet.

The purge also drops every other job waiting in the stream, including deletes
queued before the cutover. Their objects are not deleted wrongly, but some may
stay in the bucket as leftover ciphertext: a key whose claim in
`object_reconciliation_pending_v2` or `request_reconciliation_pending_v2` came
over from SQLite is skipped by reconciliation until a worker acknowledges that
job, which now never happens. Remove such leftovers by hand or with a provider
lifecycle rule.

When the count is normal, start the worker and the web front end:

```sh
kubectl -n flick scale deploy/flick-worker --replicas=1
kubectl -n flick rollout status deploy/flick-worker --timeout=180s
kubectl -n flick scale deploy/flick-web --replicas=2
kubectl -n flick rollout status deploy/flick-web --timeout=180s
```

Then apply the real overlay, so the live objects match it again and no longer
carry the zero-replica pin, remove the wrapper, and turn automated deploys back
on:

```sh
kubectl apply -k <overlay-parent>/<overlay-name>
rm -r <overlay-parent>/cutover-hold
```

### 10. Post-checks

- **Readiness.** Every flick-api replica is `Ready`, and `/readyz` answers
  `200` through a port-forward:

  ```sh
  kubectl -n flick get pods -l app.kubernetes.io/name=flick-api
  kubectl -n flick port-forward svc/flick-api 18080:8080 &
  sleep 3
  curl -fsS http://127.0.0.1:18080/readyz; kill %1
  ```

- **Reconciliation, later sweeps.** Each sweep reads one more page of the
  bucket listing. Run the step 9 tripwire query again after a few minutes. If
  the count jumps toward the object count, scale flick-api and flick-worker to
  0 immediately and investigate before anything else.

- **Smoke test.** Create a text secret and open it once; a second open must be
  blocked. Open one secret that existed before the cutover. Run a Live
  transfer between two devices to exercise both API replicas.

- **Logs.** `kubectl -n flick logs deploy/flick-api` and
  `deploy/flick-worker` show no database or migration errors, and the
  `RESTARTS` column stays at 0. A restart with `ping postgres: ...` in
  `kubectl logs --previous` means a pod could not reach PostgreSQL for 30
  seconds after it started (step 5 explains the network policy delay); check
  `NetworkPolicy/postgres` and the role passwords before going on.

## Rollback

**Before traffic resumes** (any time before step 9 starts flick-api), rollback
is safe and loses nothing. The old volumes were mounted read-only and never
written.

1. Scale the new application Deployments to 0, if any started:

   ```sh
   kubectl -n flick scale deploy/flick-api deploy/flick-worker deploy/flick-web --replicas=0
   ```

2. Re-apply the manifests of the SQLite release you recorded under
   [Preconditions](#preconditions). They mount `flick-api-data` and
   `flick-worker-data` again, with the original images and replica counts.
3. Check that the API and worker pods are `Ready` and run the smoke test.

The `postgres` and `valkey` workloads can stay; the SQLite release does not
use them. Drop the `flick_api` and `flick_worker` databases before the next
attempt, because the script refuses non-empty targets.

**After traffic resumes, the cutover is a one-way door.** Do not roll back to
SQLite:

- Secrets and requests created after the cutover exist only in PostgreSQL. The
  SQLite release would not know them, and its reconciler would delete their
  bucket objects as orphans.
- Secrets opened after the cutover were deleted from PostgreSQL, but their rows
  and inline ciphertext still exist in the SQLite file. Rolling back would make
  them openable again and break the one-time guarantee.

Fix forward on PostgreSQL instead, using [backup and restore](backup-restore.md)
for data problems.

## After The Cutover

- Take the first PostgreSQL backup ([backup and restore](backup-restore.md)).
- Keep the `flick-api-data` and `flick-worker-data` volumes and the cold
  backups only as long as you need a forensic reference, then delete them. They
  hold ciphertext of every secret that existed at cutover time, including
  secrets opened since ([PostgreSQL maintenance](postgres-maintenance.md)).

  ```sh
  kubectl -n flick delete pvc flick-api-data flick-worker-data
  ```

- Destroy the working directory (`$WORK`) on the operator machine when the
  retention period ends.
