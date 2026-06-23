# Deployment Target

Flick is designed for small self-hosted Kubernetes deployments and should be
able to run within an OCI Always Free-style environment.

## Baseline Runtime

```text
flick-web      SvelteKit frontend
flick-api      Go HTTP API
flick-worker   Go NATS worker
nats              NATS JetStream broker
```

Persistent data:

```text
api.db
worker.db
NATS JetStream filestore
S3-compatible object storage bucket for larger ciphertext
```

## Kubernetes Resources

Expected generic manifests:

```text
Namespace/flick
Deployment/flick-web
Deployment/flick-api
Deployment/flick-worker
Service/flick-web
Service/flick-api
Service/nats
StatefulSet/nats
PersistentVolumeClaim/flick-api-data
PersistentVolumeClaim/flick-worker-data
PersistentVolumeClaim/nats-data
ConfigMap/flick-config
Secret/flick-secrets
Ingress/flick
```

## Initial Resource Budget

Start small and verify with real metrics.

```text
flick-web:
  request: 32Mi memory, 10m cpu
  limit:   128Mi memory, 100m cpu

flick-api:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu

flick-worker:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu

nats:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu
```

These values are starting points, not guarantees. The deployment must be checked
with `kubectl top`, pod restarts, and real upload/download smoke tests.

## Free Tier Notes

The service avoids a managed database dependency. SQLite and NATS data can live
on Kubernetes persistent volumes, and larger encrypted files can live in
S3-compatible object storage (for example, OCI Object Storage in
S3-compatibility mode).

Operators must verify current OCI tenancy limits, Object Storage quota, request
limits, and available block volume capacity before production use.

## Scaling Boundaries

Initial replica counts:

```text
flick-web: 1
flick-api: 1
flick-worker: 1
nats: 1
```

Scaling later requires explicit review:

- API replicas need SQLite ownership and outbox behavior checked.
- Worker replicas need job receipt idempotency and NATS durable consumer
  behavior checked.
- NATS HA requires a proper clustered setup and is out of MVP scope.
