# k3s Base Manifests

`deploy/base/` contains public-safe k3s-compatible manifests for the default
Flick topology:

```text
flick-web
flick-api
flick-worker
nats
```

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
- SQLite paths
- NATS stream and subject names
- TTL and size limits
- storage backend selection

The web image is static. Browser-visible `PUBLIC_` values are embedded when the
image is built, not read from the runtime ConfigMap. The base expects web and
API traffic on the same public origin and routes `/api/*` to `flick-api`.

## Secrets

`Secret/flick-secrets` contains placeholders only:

- `FLICK_INTERNAL_TOKEN`
- `FLICK_METRICS_TOKEN`
- S3-compatible object storage settings (endpoint, region, bucket, credentials)

Replace every placeholder before production use. Public manifests must not
contain real tokens, kubeconfig, object storage credentials, bucket names,
tenancy details, or production domains.

## Persistence

The base declares three `ReadWriteOnce` PVCs:

```text
flick-api-data      /data/api.db
flick-worker-data   /data/worker.db
nats-data           /data/jetstream
```

Storage classes are left to the cluster default. S3-compatible object storage
support for larger encrypted payloads (for example, OCI Object Storage in
S3-compatibility mode) is configured separately and remains disabled in the
generic base.

## Apply

Replace placeholder images and secrets before applying. For a **fresh install**,
with no existing workers or queued jobs, apply the complete base or private overlay:

```sh
kubectl apply -k deploy/base
kubectl -n flick rollout status statefulset/nats
kubectl -n flick rollout status deploy/flick-api
kubectl -n flick rollout status deploy/flick-worker
kubectl -n flick rollout status deploy/flick-web
```

For an **existing installation** upgrading to managed-object reconciliation, all
workers must support `POST /internal/object-reconciliation/ack` before the new API
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
   404 from the missing acknowledgement endpoint keeps the NATS delivery pending
   for retry after the API upgrade.

Never roll a worker back to a version without the acknowledgement protocol while
reconciliation jobs or claims can exist. An API rollback does not remove those
jobs or claims. Select an acknowledgement-capable worker version for rollback;
do not use an unchecked `kubectl rollout undo` or reapply an older overlay that
restores a worker without acknowledgement support.

Production deployments require HTTPS at the ingress. The sample
`flick.localhost` host is for local or tutorial use only.
