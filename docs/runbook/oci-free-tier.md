---
type: Runbook
title: OCI Free Tier Deployment
description: Public-safe deployment checklist, private overlay skeleton, S3-compatible OCI/R2 setup, Cloudflare routing, smoke tests, and rollback for OCI Free Tier k3s.
tags: [runbook, oci, k3s, cloudflare]
status: stable
---

# OCI Free Tier Deployment

This runbook describes a public-safe deployment path for running Flick on a
small k3s cluster in an OCI Free Tier-style environment.

It intentionally does not contain real tenancy values, kubeconfig, registry
credentials, domains, bucket names, Object Storage credentials, internal tokens,
database files, PVC snapshots, or production overlays.

Related contracts:

- [Deployment target](../architecture/deployment-target.md)
- [Environment contract](../architecture/env-contract.md)
- [Security model](../architecture/security-model.md)
- [Storage model](../architecture/storage-model.md)
- [Container images](container-images.md)
- [k3s base manifests](k3s-base.md)

## Public And Private Boundary

The public repository provides `deploy/base/` as a generic starting point.
Real deployments need a private overlay outside this repository.

Public-safe values:

- placeholder image names
- sample hostnames such as `flick.localhost`
- non-secret resource requests and limits
- generic service names, PVC names, and environment variable names

Private values:

- kubeconfig
- OCI tenancy, compartment, namespace, bucket, and region values
- OCI API keys, config files, or workload identity details
- registry credentials when the images are private
- `FLICK_INTERNAL_TOKEN`
- `FLICK_METRICS_TOKEN`
- `FLICK_RATELIMIT_KEY_SALT`
- database passwords in `Secret/flick-db-api`, `Secret/flick-db-worker`, and
  `Secret/flick-db-admin`
- production domains, TLS issuer names, and certificate references
- production kustomize overlays
- `pg_dump` archives, PVC dumps, bucket exports, and backup archives

Keep private values in a private ops repository or a local untracked overlay.

## Images

Build and publish three images from a trusted commit:

```sh
FLICK_IMAGE_PREFIX=docker.io/<namespace> \
FLICK_IMAGE_TAG=<git-sha-or-release-tag> \
scripts/ci/images.sh

docker push docker.io/<namespace>/flick-api:<tag>
docker push docker.io/<namespace>/flick-worker:<tag>
docker push docker.io/<namespace>/flick-web:<tag>
```

Use immutable tags for production rollouts. Reusing `latest` makes rollback and
incident review harder.

Build the web image with the browser-visible API base expected by ingress. For
the default same-origin ingress path, use `/`:

```sh
FLICK_WEB_PUBLIC_API_BASE_URL=/ scripts/ci/images.sh
```

## Object Storage

Flick writes larger encrypted files through the S3 API via the AWS SDK for Go
v2, so any S3-compatible provider works. On OCI, use Object Storage in
S3-compatibility mode. Do not use MinIO as the OCI verification target. MinIO
is the local/integration-test double, not an OCI simulator.

Auth is always a static key pair: OCI Customer Secret Keys (an access/secret
key pair issued against your OCI user), because the AWS SDK cannot speak OCI
instance principal. Generate them under the OCI console
(Identity → Users → Customer Secret Keys) before first deploy.

Use a real development bucket before production. The bucket receives
browser-encrypted ciphertext only. It must not receive plaintext secrets,
passphrases, derived keys, or plaintext filenames.

Preflight checks against the S3-compatible endpoint:

```sh
oci os ns get
oci os bucket get \
  --namespace-name <object-storage-namespace> \
  --bucket-name <bucket-name>
```

Recommended bucket boundary:

- use separate development and production buckets
- keep bucket names out of the public repository
- disable public bucket access
- do not use pre-authenticated requests for normal secret delivery
- align bucket lifecycle cleanup with Flick TTL, cleanup lag, and backup policy

M8 [sender management v1](../../contracts/sender-management-v1.md) adds
List permission for the exclusive `managed/secrets/` and `managed/requests/`
prefixes (`internal/secrets/management.go`, `internal/requests/large.go`)
alongside existing Get/Put/Delete permissions. Verify pagination and late-PUT cleanup
against the configured provider. Existing lifecycle rules do not prove that
recurring API reconciliation exists or is healthy. The operator retains control
of historical versions, replicas, and retention/object-lock policies; blocked
deletion is not successful erasure. Sender reconciliation changes no private bucket setting
and adds no automatic bucket-setting gate to existing S3 startup.

Set `FLICK_STORAGE_BACKENDS` only after the bucket and Customer Secret Key
credentials are ready. Leave it empty for a database-only deployment,
where every payload stays in the `flick_api` database and the file size limit
drops to the inline threshold (`EffectiveMaxFileBytes` in
`internal/config/config.go`).

## Private Overlay

Start from `deploy/base/` and patch images, public URLs, storage backend, OCI
settings, ingress host, TLS, and any cluster-specific storage class.

Example private overlay shape:

```text
ops/flick-production/
  kustomization.yaml
  patch-configmap.yaml
  patch-secret.yaml
  patch-ingress.yaml
```

Example public-safe skeleton:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: flick

resources:
  - ../../flick-drop/deploy/base

images:
  - name: docker.io/example/flick-api
    newName: docker.io/<namespace>/flick-api
    newTag: <tag>
  - name: docker.io/example/flick-worker
    newName: docker.io/<namespace>/flick-worker
    newTag: <tag>
  - name: docker.io/example/flick-web
    newName: docker.io/<namespace>/flick-web
    newTag: <tag>

patches:
  - path: patch-configmap.yaml
  - path: patch-secret.yaml
  - path: patch-ingress.yaml
```

Do not commit the real overlay to this public repository.

## Environment Values

Patch `ConfigMap/flick-config` for deployment-specific non-secret values:

```text
FLICK_PUBLIC_BASE_URL=https://<domain>
FLICK_INTERNAL_API_BASE_URL=http://flick-api:8080
FLICK_STORAGE_BACKENDS=oci
FLICK_STORAGE_OCI_MAX_TOTAL_BYTES=18000000000
FLICK_PAYLOAD_INLINE_MAX_BYTES=1048576
FLICK_MAX_FILE_BYTES=268435456
FLICK_DEFAULT_TTL_SECONDS=3600
FLICK_MIN_TTL_SECONDS=300
FLICK_MAX_TTL_SECONDS=604800
FLICK_CSP_CONNECT_SRC=https://<object-storage-namespace>.compat.objectstorage.<oci-region>.oraclecloud.com
FLICK_TRUSTED_PROXIES=<ingress-controller-pod-cidr>
```

`FLICK_CSP_CONNECT_SRC` is read by the `flick-web` container
(`web/nginx-templates/csp.conf.template`) and appended to the CSP
`connect-src`. With object storage enabled the browser uploads large files
straight to the presigned `FLICK_STORAGE_<NAME>_ENDPOINT` URL of the backend
the API chose, so every backend's endpoint origin (`scheme://host[:port]`) must
be listed, separated by spaces, or the browser blocks the upload.

`oci` is the backend name: it is stored in every object row and delete job, so
keep it stable once objects exist (`docs/architecture/storage-model.md`).
`FLICK_STORAGE_OCI_MAX_TOTAL_BYTES=18000000000` caps placement at 90 percent of
the 20 GB Always Free Object Storage allowance; with a single backend `0`
(unlimited) is also accepted.

`FLICK_TRUSTED_PROXIES` is the comma-separated CIDR list whose peers may set
`X-Forwarded-For` (`internal/config/config.go`,
`internal/httpapi/ratelimit.go`). Behind an ingress controller the API's direct
peer is always the controller pod, so leaving it empty makes every client share
one per-minute rate-limit bucket for create and open. Set it to the pod CIDR
that the ingress controller runs in (on a default k3s cluster that is the
cluster CIDR `10.42.0.0/16`), and only to addresses that client traffic cannot
reach directly. When a CDN or reverse proxy (for example Cloudflare)
sits in front of the ingress, also list that provider's published egress ranges,
so the API reads the real client address from the `X-Forwarded-For` hop the
CDN added instead of bucketing every visitor under the CDN's address.

The base `ConfigMap/flick-config` already carries the in-cluster addresses that
a single-cluster deployment keeps as is: `FLICK_VALKEY_ADDR=valkey:6379`,
`FLICK_INTERNAL_HTTP_PORT=8081` (the live-transfer stream listener flick-api
replicas dial pod to pod), and `FLICK_NATS_URL=nats://nats:4222`. It holds no
database DSN; see the database Secrets below.

Patch `Secret/flick-secrets` for sensitive values:

```text
FLICK_INTERNAL_TOKEN=<private-random-token>
FLICK_METRICS_TOKEN=<private-random-token>
FLICK_RATELIMIT_KEY_SALT=<private-random-salt>
FLICK_STORAGE_OCI_ENDPOINT=https://<object-storage-namespace>.compat.objectstorage.<oci-region>.oraclecloud.com
FLICK_STORAGE_OCI_REGION=<oci-region>
FLICK_STORAGE_OCI_BUCKET=<bucket-name>
FLICK_STORAGE_OCI_ACCESS_KEY_ID=<oci-customer-secret-key-id>
FLICK_STORAGE_OCI_SECRET_ACCESS_KEY=<oci-customer-secret-key-secret>
FLICK_STORAGE_OCI_PATH_STYLE=true
```

The access/secret pair is an OCI Customer Secret Key issued against your OCI
user. Do not commit Customer Secret Key values, API signing keys, config files,
private key material, or base64-encoded versions of those values.

`FLICK_RATELIMIT_KEY_SALT` must be the same on every flick-api replica;
rotating it resets in-flight rate-limit buckets and orphans live-transfer rooms
(`docs/architecture/env-contract.md`).

Patch the three database Secrets with private random values. Use URL-safe
passwords (for example `openssl rand -hex 24`), because each pod places its
password in a `postgres://` URL as is (`deploy/base/api.yaml`,
`deploy/base/worker.yaml`):

```text
Secret/flick-db-api      password=<private-random-password>
Secret/flick-db-worker   password=<private-random-password>
Secret/flick-db-admin    POSTGRES_PASSWORD=<private-random-password>
```

flick-api builds `FLICK_DATABASE_URL` and flick-worker builds
`FLICK_WORKER_DATABASE_URL` from their own role password; neither reads the
other's Secret or the superuser password. The postgres pod creates the
`flick_api` and `flick_worker` roles from these values only on the first start
of an empty `postgres-data` volume (`ConfigMap/postgres-init` in
`deploy/base/postgres.yaml`), so set them before the first apply.

## PVC And Resource Budget

The base declares two `ReadWriteOnce` PVCs (`deploy/base/pvc.yaml`):

```text
postgres-data   2Gi  PostgreSQL data directory (flick_api and flick_worker)
nats-data       1Gi  /data/jetstream
```

flick-api, flick-worker, flick-web, and Valkey mount no persistent volume.
flick-api mounts a `600Mi` `emptyDir` at `/tmp` for request finalize spools
(`deploy/base/api.yaml`); that space comes from the node's ephemeral storage.

The base resource budget is intentionally small (per pod):

```text
flick-web      x2  32Mi request, 128Mi limit, 10m request, 100m limit
flick-api      x2  64Mi request, 256Mi limit, 25m request, 250m limit
flick-worker   x1  64Mi request, 256Mi limit, 25m request, 250m limit
postgres       x1  128Mi request, 512Mi limit, 50m request, 500m limit
valkey         x1  16Mi request, 128Mi limit, 10m request, 250m limit
nats           x1  64Mi request, 256Mi limit, 25m request, 250m limit
```

Valkey also caps its own data at `--maxmemory 96mb` with
`--maxmemory-policy noeviction` (`deploy/base/valkey.yaml`): when full it
refuses writes instead of dropping a live waiting room.

Before production, verify current OCI compute, memory, block volume, and Object
Storage limits in the target tenancy. Free Tier policies and quotas can change.

## Cluster Preflight

Check cluster access and default infrastructure:

```sh
kubectl config current-context
kubectl get nodes -o wide
kubectl get storageclass
kubectl get ingressclass
kubectl auth can-i create deployment -n flick
kubectl auth can-i create secret -n flick
kubectl auth can-i create persistentvolumeclaim -n flick
```

Check capacity before applying:

```sh
kubectl top nodes
kubectl describe nodes
```

If metrics are unavailable, install or repair metrics-server before relying on
resource budget decisions.

Render and review the private overlay:

```sh
kustomize build <private-overlay>
kustomize build <private-overlay> | kubectl apply --dry-run=client --validate=false -f -
```

Inspect rendered output before applying. It must not contain placeholder images,
`replace-me-before-use`, `flick.localhost`, empty OCI values, or unexpected
private data.

## Apply

For a fresh install, apply the private overlay. An existing installation still
on the SQLite layout moves its data once through
[PostgreSQL cutover](postgres-cutover.md) before applying this layout. For an
existing installation, also first complete the
[worker-first upgrade procedure](k3s-base.md#apply): every old
worker, including terminating pods, must be gone before applying an API that
enables managed-object reconciliation. Do not apply the complete overlay before
that prerequisite is satisfied.

```sh
kubectl apply -k <private-overlay>
```

Wait for readiness:

```sh
kubectl -n flick rollout status deploy/postgres --timeout=180s
kubectl -n flick rollout status deploy/valkey --timeout=180s
kubectl -n flick rollout status statefulset/nats --timeout=180s
kubectl -n flick rollout status deploy/flick-api --timeout=180s
kubectl -n flick rollout status deploy/flick-worker --timeout=180s
kubectl -n flick rollout status deploy/flick-web --timeout=180s
```

Check endpoints:

```sh
kubectl -n flick port-forward service/flick-api 18080:8080
curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz
```

Then test through public HTTPS ingress:

```sh
curl -fsS https://<domain>/healthz
curl -fsS https://<domain>/api/config
```

`/healthz` on the public host is answered by the `flick-web` nginx
(`web/nginx.conf`), and `/api/config` proves that ingress routes `/api` to
`flick-api` (`deploy/base/ingress.yaml`). The API registers its `/healthz` and
`/readyz` probes at the root path only (`internal/httpapi/router.go`), so
`https://<domain>/api/healthz` and `/api/readyz` return 404; check API readiness
through the port-forward above.

Production deployments require HTTPS at ingress. Plain public HTTP is not an
acceptable production transport.

## Operational Checks

Readiness and restarts:

```sh
kubectl -n flick get pods -o wide
kubectl -n flick get deploy,statefulset,svc,ingress,pvc
kubectl -n flick describe pod -l app.kubernetes.io/name=flick-api
kubectl -n flick describe pod -l app.kubernetes.io/name=postgres
kubectl -n flick describe pod -l app.kubernetes.io/name=nats
```

Logs (`deploy/flick-api` picks one replica; use the label selector to read all
of them):

```sh
kubectl -n flick logs -l app.kubernetes.io/name=flick-api --tail=200 --prefix
kubectl -n flick logs deploy/flick-worker --tail=200
kubectl -n flick logs deploy/postgres --tail=200
kubectl -n flick logs deploy/valkey --tail=200
kubectl -n flick logs statefulset/nats --tail=200
```

Resource use:

```sh
kubectl -n flick top pods
kubectl -n flick top pod -l app.kubernetes.io/name=flick-api
kubectl -n flick top pod -l app.kubernetes.io/name=flick-worker
kubectl -n flick top pod -l app.kubernetes.io/name=postgres
kubectl -n flick top pod -l app.kubernetes.io/name=nats
```

Storage use:

```sh
kubectl -n flick exec deploy/postgres -- df -h /var/lib/postgresql/data
kubectl -n flick exec deploy/postgres -- psql -U postgres -c \
  "SELECT datname, pg_size_pretty(pg_database_size(datname)) FROM pg_database WHERE datname LIKE 'flick_%';"
kubectl -n flick exec statefulset/nats -- df -h /data
```

Deleted ciphertext can remain in PostgreSQL dead tuples until `VACUUM`
reclaims them, and in WAL segments until they are recycled. Autovacuum handles
routine cleanup; see [PostgreSQL maintenance](postgres-maintenance.md) for the
checks, manual `VACUUM`, and disk-space limits, and
[backup and restore](backup-restore.md) before running anything that rewrites
tables.

## Edge Proxy and Cloudflare Routing

When deploying Flick behind Cloudflare or a similar CDN/edge proxy in front of
an OCI instance (for example, in the Osaka `ap-osaka-1` region), streaming large
encrypted payloads (up to `FLICK_MAX_FILE_BYTES`, default 256 MiB) across
international edge-to-origin transit routes requires specific proxy settings.

### Edge-to-Origin Download Slowdowns

Without traffic optimization, edge PoPs distant from the origin (e.g., Hong
Kong `HKG` or US edges reaching an Osaka origin) can experience intermittent
single-stream throughput collapse down to ~300 KB/s due to cross-border public
transit congestion and TCP packet loss. At 300 KB/s, a 256 MiB download takes
~15 minutes, approaching the 30-minute streaming limit
(`secrets.OpenStreamTimeout`, `requests.OpenStreamTimeout`).

### Recommended Cloudflare Configuration

1. **Cache Rules for Open Endpoints**:
   Add a Cache Rule matching secret and request open paths:
   - Match: `(http.request.uri.path contains "/api/" and http.request.uri.path ends_with "/open")`.
   - Cache Eligibility: **Bypass Cache**. Secret and request ciphertext payloads
     are one-time ephemeral streams; caching must be bypassed.
2. **Configuration Rules for Response Body Buffering**:
   Under Rules → Configuration Rules, add a rule matching the same open paths:
   - Match: `(http.request.uri.path contains "/api/" and http.request.uri.path ends_with "/open")`.
   - Response Body Buffering: **None / Off**. Disabling response body buffering
     ensures ciphertext streams directly to the client without edge delay or
     buffer accumulation.
3. **Argo Smart Routing**:
   Enable Cloudflare Argo Smart Routing for the zone. Argo routes edge-to-origin
   transit across Cloudflare's private backbone instead of congested public
   transit, stabilizing cross-border throughput between regional PoPs and the
   origin.
4. **Host TCP Congestion Control (BBR)**:
   On the OCI host, enable BBR congestion control to improve single-stream
   throughput recovery over lossy international transit:
   ```sh
   sudo sysctl -w net.core.default_qdisc=fq
   sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
   ```
5. **Additional Object Backend (Cloudflare R2)**:
   Flick can run a Cloudflare R2 bucket in parallel with the OCI bucket by
   naming both backends (`FLICK_STORAGE_BACKENDS=oci,r2` plus the
   `FLICK_STORAGE_R2_*` variables in
   [Environment contract](../architecture/env-contract.md#object-storage-backends),
   with `FLICK_STORAGE_R2_REGION=auto`). With several backends every
   `FLICK_STORAGE_<NAME>_MAX_TOTAL_BYTES` must be positive, for example
   `18000000000` for OCI and `9000000000` for R2. Each new object goes to a
   backend whose ceiling still fits it, chosen at random weighted by remaining
   space; existing objects never move (`Registry.Choose` in
   `internal/storage/registry.go`). Add the R2 endpoint origin to
   `FLICK_CSP_CONNECT_SRC`. Opens still
   stream ciphertext through the API (`internal/storage/object.go`), so R2 adds
   free-tier capacity, not a download path that bypasses the origin.

## Smoke Test

Use the application UI to create and open:

- a short text secret
- a small file below `FLICK_PAYLOAD_INLINE_MAX_BYTES`
- a file above `FLICK_PAYLOAD_INLINE_MAX_BYTES` when object storage is enabled

The large-file test must use the real development or production OCI bucket in
S3-compatibility mode. Success against a local MinIO does not prove OCI behavior.

Verify that consumed secrets cannot be opened again and that five invalid
passphrase attempts consume and remove the secret.

## Rollback

Inspect Kubernetes rollout history before selecting rollback images:

```sh
kubectl -n flick rollout history deploy/flick-api
kubectl -n flick rollout history deploy/flick-worker
kubectl -n flick rollout history deploy/flick-web
```

Keep a worker that acknowledges both managed namespaces whenever object
reconciliation
jobs or claims can exist. Rolling back the API does not clear those jobs or
claims. Never restore an older worker without the acknowledgement protocol;
select a compatible worker image explicitly instead of an unchecked worker
`rollout undo`. See the [upgrade and rollback constraints](k3s-base.md#apply).

Releases from before the PostgreSQL cutover read SQLite files from the removed
`flick-api-data` and `flick-worker-data` volumes, so an API or worker image
from that era cannot run on this layout; see
[PostgreSQL cutover](postgres-cutover.md) for going back across the cutover.

After checking compatibility, roll back API and web images to selected revisions:

```sh
kubectl -n flick rollout undo deploy/flick-api --to-revision=<api-revision>
kubectl -n flick rollout undo deploy/flick-web --to-revision=<web-revision>
```

If the issue is caused by configuration, revert the private overlay commit and
reapply only after ensuring its worker image still supports acknowledgements:

```sh
kustomize build <private-overlay> | kubectl apply --dry-run=client --validate=false -f -
kubectl apply -k <private-overlay>
```

Avoid deleting PVCs or buckets as a rollback step. PVC and bucket deletion is a
data-destruction operation, not a normal rollback.

## Residual Risk

Flick does not know plaintext, passphrases, or derived keys, but deployment
operators still control infrastructure risk.

Known residual risks:

- OCI quotas, Free Tier eligibility, and regional capacity can change.
- PostgreSQL dead tuples and WAL segments, PVC snapshots, and `pg_dump`
  archives may retain old ciphertext after logical deletion.
- A compromised web image can steal plaintext before encryption or after
  decryption.
- A compromised cluster can delete data early, deny service, or serve malicious
  assets.
- Optional k3d smoke does not prove OCI Object Storage in S3-compatibility mode,
  ingress TLS, DNS, or production storage classes.

Document backup retention, bucket lifecycle rules, and incident rollback steps
in the private ops repository for each real deployment.
