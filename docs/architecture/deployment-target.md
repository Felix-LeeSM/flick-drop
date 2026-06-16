# Deployment Target

BurnLink is designed for small self-hosted Kubernetes deployments and should be
able to run within an OCI Always Free-style environment.

## Baseline Runtime

```text
burnlink-web      SvelteKit frontend
burnlink-api      Go HTTP API
burnlink-worker   Go NATS worker
nats              NATS JetStream broker
```

Persistent data:

```text
api.db
worker.db
NATS JetStream filestore
OCI Object Storage bucket for larger ciphertext
```

## Kubernetes Resources

Expected generic manifests:

```text
Namespace/burnlink
Deployment/burnlink-web
Deployment/burnlink-api
Deployment/burnlink-worker
Service/burnlink-web
Service/burnlink-api
Service/nats
StatefulSet/nats
PersistentVolumeClaim/burnlink-data
PersistentVolumeClaim/nats-data
ConfigMap/burnlink-config
Secret/burnlink-secrets
Ingress/burnlink
```

## Initial Resource Budget

Start small and verify with real metrics.

```text
burnlink-web:
  request: 32Mi memory, 10m cpu
  limit:   128Mi memory, 100m cpu

burnlink-api:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu

burnlink-worker:
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
on Kubernetes persistent volumes, and larger encrypted files can live in OCI
Object Storage.

Operators must verify current OCI tenancy limits, Object Storage quota, request
limits, and available block volume capacity before production use.

## Scaling Boundaries

Initial replica counts:

```text
burnlink-web: 1
burnlink-api: 1
burnlink-worker: 1
nats: 1
```

Scaling later requires explicit review:

- API replicas need SQLite ownership and outbox behavior checked.
- Worker replicas need job receipt idempotency and NATS durable consumer
  behavior checked.
- NATS HA requires a proper clustered setup and is out of MVP scope.
