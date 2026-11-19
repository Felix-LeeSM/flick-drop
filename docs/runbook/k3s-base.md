---
type: Runbook
title: k3s Base Manifests
description: Validation, image/secret overrides, persistence, and worker-first reconciliation upgrade procedure for deploy/base/.
tags: [runbook, k3s, manifests]
status: stable
---

# k3s Base Manifests

`deploy/base/` contains public-safe k3s-compatible manifests for the default
Flick topology:

| Workload | Kind | Replicas | Manifest |
| --- | --- | --- | --- |
| `flick-web` | Deployment | 2 | `deploy/base/web.yaml` |
| `flick-api` | Deployment | 2 | `deploy/base/api.yaml` |
| `flick-worker` | Deployment | 1 | `deploy/base/worker.yaml` |
| `postgres` | Deployment (`strategy: Recreate`) | 1 | `deploy/base/postgres.yaml` |
| `valkey` | Deployment (`strategy: Recreate`) | 1 | `deploy/base/valkey.yaml` |
| `nats` | StatefulSet | 1 | `deploy/base/nats.yaml` |

flick-api and flick-worker keep no local state: both databases live in the
`postgres` pod, so either Deployment can run more than one replica.
`postgres:17-alpine` holds the `flick_api` and `flick_worker` logical databases,
each owned by its own role. `valkey/valkey:8.1-alpine` holds only in-memory
rate-limit buckets and live-transfer waiting rooms. NetworkPolicies limit
`Service/postgres` to flick-api and flick-worker pods and `Service/valkey` to
flick-api pods.

The base is intentionally generic. It uses placeholder images, placeholder
secret values, and `flick.localhost` as the sample ingress host.

## Validate

Render and structurally validate the base:

```sh
mise run manifests
```

Equivalent render command:

```sh
kustomize build deploy/base
```

When a Kubernetes API is reachable, run an additional client dry run:

```sh
kustomize build deploy/base | kubectl apply --dry-run=client --validate=false -f -
```

These checks do not prove that images exist, that a cluster can pull them, or
that ingress and storage classes are correct for a specific cluster.

The k3d smoke test uses the separate `deploy/k3d/` overlay. The generic base is
not used directly for smoke tests because it intentionally contains placeholder
image names.

## Images

The base references placeholder images:

```text
docker.io/example/flick-web:latest
docker.io/example/flick-api:latest
docker.io/example/flick-worker:latest
```

Before applying to a real cluster, set images with a private overlay or a local
one-off kustomize edit:

```sh
kustomize edit set image \
  docker.io/example/flick-web=docker.io/<namespace>/flick-web:<tag> \
  docker.io/example/flick-api=docker.io/<namespace>/flick-api:<tag> \
  docker.io/example/flick-worker=docker.io/<namespace>/flick-worker:<tag>
```

Do not commit deployment-specific registry names when they reveal private
deployment details.

For an end-to-end OCI Free Tier-style deployment checklist, use
[OCI Free Tier deployment](oci-free-tier.md).

## Config

`ConfigMap/flick-config` contains non-secret runtime defaults:

- public and internal service URLs
- `FLICK_VALKEY_ADDR` (`valkey:6379`) and `FLICK_INTERNAL_HTTP_PORT` (`8081`,
  the live-transfer stream listener that flick-api replicas dial pod to pod)
- NATS stream and subject names
- TTL and size limits
- object storage backend names (`FLICK_STORAGE_BACKENDS`, empty in the base,
  which keeps every payload inline)
- rate limit and proxy trust (`FLICK_OPEN_RATE_PER_MIN`,
  `FLICK_TRUSTED_PROXIES`)
- `FLICK_CSP_CONNECT_SRC`, the extra CSP `connect-src` origins that the
  `flick-web` container renders into nginx at start
  (`web/nginx-templates/csp.conf.template`, wired in `deploy/base/web.yaml`).
  The base value is empty, which is correct while `FLICK_STORAGE_BACKENDS` is
  empty; a deployment that names backends there must list every
  `FLICK_STORAGE_<NAME>_ENDPOINT` origin or the browser blocks direct presigned
  uploads.

The web image is static. Browser-visible `PUBLIC_` values are embedded when the
image is built, not read from the runtime ConfigMap. The base expects web and
API traffic on the same public origin and routes `/api/*` to `flick-api`.

## Secrets

`deploy/base/secret.yaml` declares four Secrets, all with
`replace-me-before-use` placeholders:

| Secret | Key | Read by |
| --- | --- | --- |
| `flick-secrets` | `FLICK_INTERNAL_TOKEN`, `FLICK_METRICS_TOKEN`, `FLICK_RATELIMIT_KEY_SALT` | flick-api and flick-worker (`envFrom`) |
| `flick-db-api` | `password` | flick-api, and the postgres init script |
| `flick-db-worker` | `password` | flick-worker, and the postgres init script |
| `flick-db-admin` | `POSTGRES_PASSWORD` | the postgres pod only (superuser); no Flick process uses it |

No database DSN is stored anywhere. Each pod builds its own from its role's
password: `deploy/base/api.yaml` sets
`FLICK_DATABASE_URL=postgres://flick_api:$(FLICK_API_DB_PASSWORD)@postgres:5432/flick_api?sslmode=disable`,
and `deploy/base/worker.yaml` sets the matching `FLICK_WORKER_DATABASE_URL` for
`flick_worker`. Because the password is placed in a URL as is, generate
URL-safe values, for example `openssl rand -hex 24`.

`ConfigMap/postgres-init` in `deploy/base/postgres.yaml` creates the
`flick_api` and `flick_worker` roles with those passwords only on the first
start of an empty `postgres-data` volume. Changing `Secret/flick-db-api` or
`Secret/flick-db-worker` later does not change the role password; change it in
PostgreSQL as well (`ALTER ROLE flick_api PASSWORD '...'`), then restart the
pods that read it.

`FLICK_RATELIMIT_KEY_SALT` keys the Valkey rate-limit buckets and live-transfer
rooms; every flick-api replica must share the same value
(`docs/architecture/env-contract.md`).

The base has no object storage settings because `FLICK_STORAGE_BACKENDS` is
empty. A deployment that enables object storage sets `FLICK_STORAGE_BACKENDS`
and adds each backend's `FLICK_STORAGE_<NAME>_*` values (keys in the Secret) in
its private overlay; see [OCI Free Tier deployment](oci-free-tier.md#environment-values).

Replace every placeholder before production use. Public manifests must not
contain real tokens, kubeconfig, object storage credentials, bucket names,
tenancy details, or production domains.

## Persistence

`deploy/base/pvc.yaml` declares two `ReadWriteOnce` PVCs:

```text
postgres-data   2Gi  /var/lib/postgresql/data (PGDATA=/var/lib/postgresql/data/pgdata)
nats-data       1Gi  /data/jetstream
```

flick-api, flick-worker, flick-web, and Valkey mount no persistent volume.
`postgres-data` is `ReadWriteOnce`, which is why `Deployment/postgres` uses
`strategy: Recreate`: the old pod must stop before the new one mounts the
volume. Backups are `pg_dump` archives; see
[Backup and restore](backup-restore.md).

Storage classes are left to the cluster default. S3-compatible object storage
support for larger encrypted payloads (for example, OCI Object Storage in
S3-compatibility mode) is configured separately and remains disabled in the
generic base.

## Apply

Replace placeholder images and secrets before applying. For a **fresh install**,
with no existing workers or queued jobs, apply the complete base or private overlay:

```sh
kubectl apply -k deploy/base
kubectl -n flick rollout status deploy/postgres
kubectl -n flick rollout status deploy/valkey
kubectl -n flick rollout status statefulset/nats
kubectl -n flick rollout status deploy/flick-api
kubectl -n flick rollout status deploy/flick-worker
kubectl -n flick rollout status deploy/flick-web
```

An **existing installation still on the SQLite layout** (`flick-api-data` and
`flick-worker-data` volumes) moves its data once through
[PostgreSQL cutover](postgres-cutover.md) instead of applying this base
directly. Starting flick-api against an empty `flick_api` database while the
bucket still holds objects lets object reconciliation delete live bodies as
orphans ([Backup and restore](backup-restore.md#why-flick-api-stays-down-until-the-restore-finishes)).

For an **existing installation** upgrading to managed-object reconciliation, all
workers must support `POST /internal/object-reconciliation/ack` for **both**
`managed/secrets/` and `managed/requests/` before the new API
scanner starts. Applying the complete overlay first is unsafe: an old worker can
acknowledge a deletion in NATS without releasing the API claim, permanently
preventing cleanup of a later PUT to the same key.

1. Record the existing worker pod names and update the private overlay to the
   intended immutable image versions. Do not apply the complete overlay yet.

   ```sh
   kubectl -n flick get pods -l app.kubernetes.io/name=flick-worker -o wide
   kubectl -n flick set image deployment/flick-worker worker=<new-worker-image-by-digest>
   kubectl -n flick rollout status deployment/flick-worker --timeout=180s
   ```

2. Wait until **every old worker pod is deleted**, including terminating pods.
   Inspect the complete pod list again; rollout readiness alone does not establish
   that old processes have stopped. Also replace any worker consumers outside
   `Deployment/flick-worker`. Stop the upgrade if any old worker remains.

   ```sh
   kubectl -n flick get pods -l app.kubernetes.io/name=flick-worker -o wide
   ```

3. After all old workers are gone, apply the prepared base or private overlay and
   wait for API, worker, and web readiness using the commands for a fresh install.
   The updated worker can run against the old API during this interval: an HTTP
   404 from a missing acknowledgement endpoint (or 400 for the request prefix
   on an older API) keeps the NATS delivery pending
   for retry after the API upgrade.

Never roll a worker back to a version without the acknowledgement protocol while
reconciliation jobs or claims can exist. An API rollback does not remove those
jobs or claims. Select a worker version that acknowledges both managed namespaces
for rollback;
do not use an unchecked `kubectl rollout undo` or reapply an older overlay that
restores a worker without acknowledgement support.

Production deployments require HTTPS at the ingress. The sample
`flick.localhost` host is for local or tutorial use only.
