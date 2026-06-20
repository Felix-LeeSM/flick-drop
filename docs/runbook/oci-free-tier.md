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
- production domains, TLS issuer names, and certificate references
- production kustomize overlays
- SQLite databases, PVC dumps, bucket exports, and backup archives

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

## OCI Object Storage

Flick treats OCI Object Storage as a real deployment dependency for larger
encrypted files. Do not use MinIO as the OCI verification target. MinIO is
S3-compatible, but it is not an OCI simulator.

Use a real development bucket before production. The bucket receives
browser-encrypted ciphertext only. It must not receive plaintext secrets,
passphrases, derived keys, or plaintext filenames.

Preflight checks:

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

Set `FLICK_STORAGE_LARGE_BACKEND=oci` only after the bucket and credentials are
ready. Leave it `disabled` for SQLite-only deployments.

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
FLICK_API_BASE_URL=https://<domain>
FLICK_INTERNAL_API_BASE_URL=http://flick-api:8080
FLICK_STORAGE_LARGE_BACKEND=oci
FLICK_PAYLOAD_INLINE_MAX_BYTES=1048576
FLICK_MAX_FILE_BYTES=26214400
FLICK_DEFAULT_TTL_SECONDS=3600
FLICK_MIN_TTL_SECONDS=300
FLICK_MAX_TTL_SECONDS=604800
FLICK_WORKER_CONCURRENCY=2
```

Patch `Secret/flick-secrets` for sensitive values:

```text
FLICK_INTERNAL_TOKEN=<private-random-token>
FLICK_OCI_AUTH_MODE=instance_principal
FLICK_OCI_REGION=<oci-region>
FLICK_OCI_NAMESPACE=<object-storage-namespace>
FLICK_OCI_BUCKET=<bucket-name>
FLICK_OCI_COMPARTMENT_OCID=<compartment-ocid>
```

If the cluster cannot use instance principals, use a private secret mechanism
for OCI API key configuration. Do not commit API keys, config files, private
key material, or base64-encoded versions of those values.

## PVC And Resource Budget

The base starts with three `ReadWriteOnce` PVCs:

```text
flick-api-data      1Gi  /data/api.db
flick-worker-data   1Gi  /data/worker.db
nats-data           1Gi  /data/jetstream
```

The base resource budget is intentionally small:

```text
flick-web      32Mi request, 128Mi limit, 10m request, 100m limit
flick-api      64Mi request, 256Mi limit, 25m request, 250m limit
flick-worker   64Mi request, 256Mi limit, 25m request, 250m limit
nats           64Mi request, 256Mi limit, 25m request, 250m limit
```

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

Apply the private overlay:

```sh
kubectl apply -k <private-overlay>
```

Wait for readiness:

```sh
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
curl -fsS https://<domain>/api/healthz
curl -fsS https://<domain>/api/readyz
curl -fsS https://<domain>/healthz
```

Production deployments require HTTPS at ingress. Plain public HTTP is not an
acceptable production transport.

## Operational Checks

Readiness and restarts:

```sh
kubectl -n flick get pods -o wide
kubectl -n flick get deploy,statefulset,svc,ingress,pvc
kubectl -n flick describe pod -l app.kubernetes.io/name=flick-api
kubectl -n flick describe pod -l app.kubernetes.io/name=nats
```

Logs:

```sh
kubectl -n flick logs deploy/flick-api --tail=200
kubectl -n flick logs deploy/flick-worker --tail=200
kubectl -n flick logs statefulset/nats --tail=200
```

Resource use:

```sh
kubectl -n flick top pods
kubectl -n flick top pod -l app.kubernetes.io/name=flick-api
kubectl -n flick top pod -l app.kubernetes.io/name=flick-worker
kubectl -n flick top pod -l app.kubernetes.io/name=nats
```

Storage use:

```sh
kubectl -n flick exec deploy/flick-api -- df -h /data
kubectl -n flick exec deploy/flick-worker -- df -h /data
kubectl -n flick exec statefulset/nats -- df -h /data
```

SQLite maintenance requires care because deleted ciphertext can remain in WAL
or freelist pages until checkpoint or vacuum. Do not run manual maintenance
commands without a backup and a maintenance window.

## Smoke Test

Use the application UI to create and open:

- a short text secret
- a small file below `FLICK_PAYLOAD_INLINE_MAX_BYTES`
- a file above `FLICK_PAYLOAD_INLINE_MAX_BYTES` when OCI storage is enabled

The large-file test must use the real development or production OCI bucket.
Success against a local S3-compatible service does not prove OCI behavior.

Verify that consumed secrets cannot be opened again and that five invalid
passphrase attempts consume and remove the secret.

## Rollback

Rollback images with Kubernetes rollout history:

```sh
kubectl -n flick rollout history deploy/flick-api
kubectl -n flick rollout history deploy/flick-worker
kubectl -n flick rollout history deploy/flick-web

kubectl -n flick rollout undo deploy/flick-api
kubectl -n flick rollout undo deploy/flick-worker
kubectl -n flick rollout undo deploy/flick-web
```

If the issue is caused by configuration, revert the private overlay commit and
reapply:

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
- SQLite WAL, freelist pages, PVC snapshots, and backups may retain old
  ciphertext after logical deletion.
- A compromised web image can steal plaintext before encryption or after
  decryption.
- A compromised cluster can delete data early, deny service, or serve malicious
  assets.
- Optional k3d smoke does not prove OCI Object Storage, ingress TLS, DNS, or
  production storage classes.

Document backup retention, bucket lifecycle rules, and incident rollback steps
in the private ops repository for each real deployment.
