# Deployment Target

Flick is designed for small self-hosted Kubernetes deployments and should be
able to run within an OCI Always Free-style environment.

## Baseline Runtime

```text
flick-web      SvelteKit frontend
flick-api      Go HTTP API
flick-worker   Go NATS worker
postgres          PostgreSQL 17, two logical databases (flick_api, flick_worker)
valkey            Valkey 8, non-persistent shared state
nats              NATS JetStream broker
```

Persistent data:

```text
PostgreSQL data volume (flick_api and flick_worker databases)
NATS JetStream filestore
S3-compatible object storage bucket for larger ciphertext
```

Valkey persists nothing by contract (`--save "" --appendonly no`,
`security-model.md`). The API and worker own no local volume: M15 removed
`flick-api-data` / `flick-worker-data` so `flick-api` can run `replicas: 2`.

## Kubernetes Resources

Expected generic manifests (`deploy/base/kustomization.yaml`):

```text
Namespace/flick
Deployment/flick-web
Deployment/flick-api
Deployment/flick-worker
Deployment/postgres
Deployment/valkey
ConfigMap/postgres-init
Service/flick-web
Service/flick-api
Service/flick-api-headless
Service/flick-worker-metrics
Service/flick-worker-metrics-headless
Service/postgres
Service/valkey
Service/nats
Service/nats-headless
StatefulSet/nats
PersistentVolumeClaim/postgres-data
PersistentVolumeClaim/nats-data
ConfigMap/flick-config
Secret/flick-secrets
Secret/flick-db-api
Secret/flick-db-worker
Secret/flick-db-admin
NetworkPolicy/flick-api
NetworkPolicy/postgres
NetworkPolicy/valkey
Ingress/flick
```

`Service/flick-worker-metrics` (`deploy/base/worker.yaml`) is a ClusterIP
Service for the worker's bearer-guarded `/metrics` listener on port `9091`.
`Service/nats-headless` (`deploy/base/nats.yaml`) is the headless governing
Service of `StatefulSet/nats`; clients connect through `Service/nats`.
`Service/flick-api-headless` and `Service/flick-worker-metrics-headless`
back VictoriaMetrics per-pod `dns_sd_configs` scraping (`deploy/base/monitoring/vmsingle.yaml`).
`Secret/flick-db-api` and `Secret/flick-db-worker` each hold one role
password. `deploy/base/api.yaml` builds `FLICK_DATABASE_URL` from the
`flick_api` password and `deploy/base/worker.yaml` builds
`FLICK_WORKER_DATABASE_URL` from the `flick_worker` password, so neither pod
can reach the other role's credentials. `Secret/flick-db-admin` holds the
postgres superuser password, which no Flick process uses. `ConfigMap/postgres-init`
creates both roles and databases on the first start of an empty
`postgres-data` volume. The three NetworkPolicies limit PostgreSQL to the API
and worker pods, Valkey to the API pods, and the API's live-transfer stream
listener (`FLICK_INTERNAL_HTTP_PORT`) to other API pods.

Opt-in monitoring lives in `deploy/base/monitoring/` and is applied separately
with `kubectl apply -k deploy/base/monitoring`; `deploy/base/kustomization.yaml`
does not include it. It adds kube-state-metrics, node-exporter, VictoriaMetrics
single (`vmsingle`), Loki, two Alloy DaemonSets (`alloy` for logs,
`alloy-cadvisor` for container usage), and Grafana with its own
`Ingress/grafana`. Its components, scrape targets, and resource requests are
documented in `deploy/base/monitoring/README.md`.

## Initial Resource Budget

Start small and verify with real metrics.

```text
flick-web:
  request: 32Mi memory, 10m cpu
  limit:   128Mi memory, 100m cpu

flick-api:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu
  /tmp:    600Mi emptyDir on node disk (two request finalize spools)

flick-worker:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu

postgres:
  request: 128Mi memory, 50m cpu
  limit:   512Mi memory, 500m cpu

valkey:
  request: 16Mi memory, 10m cpu
  limit:   128Mi memory, 250m cpu

nats:
  request: 64Mi memory, 25m cpu
  limit:   256Mi memory, 250m cpu
```

These values are starting points, not guarantees. The deployment must be checked
with `kubectl top`, pod restarts, and real upload/download smoke tests.

Rollout and spread (`deploy/base/api.yaml`, `deploy/base/web.yaml`):
`flick-api` and `flick-web` use `RollingUpdate` with `maxSurge: 0,
maxUnavailable: 1`, so a rollout never runs more pods than the replica count
(no surge pod needs spare memory), plus a 5-second `preStop` sleep. `flick-api` also sets
`terminationGracePeriodSeconds: 60` for live-transfer draining
(`cmd/flick-api/main.go` `shutdownTimeout`, 50 seconds). Both carry a soft
`topologySpreadConstraints` on `kubernetes.io/hostname` that prefers one
replica per node and still schedules on a single-node cluster.

`deploy/base` names no node. Pinning workloads to particular nodes belongs in
a deployment's private overlay. The public demo instance's overlay places the
data tier (postgres, valkey, nats), `flick-worker`, and one `flick-api` +
`flick-web` replica on its larger Osaka node (arm64, ~5.9 GiB) and the second
`flick-api` + `flick-web` replica on the smaller Osaka node (amd64,
~952 MiB), keeps all three off its Korea-region nodes (cross-region RTT to the
Osaka Postgres/NATS/S3 tier would tax every request), and moves `loki` to the
larger node to free memory on the smaller one.

## Free Tier Notes

The service still avoids a managed database dependency: one PostgreSQL 17 pod
on a Kubernetes persistent volume holds both logical databases, and larger
encrypted files live in S3-compatible object storage (for example, OCI Object
Storage in S3-compatibility mode). An optional second S3-compatible backend
(for example Cloudflare R2) can run in parallel with the primary to pool
provider free tiers; see `docs/architecture/storage-model.md` and
`env-contract.md`. Valkey runs memory-only and adds no persistence cost.

Operators must verify current OCI tenancy limits, Object Storage quota, request
limits, and available block volume capacity before production use, plus the
corresponding free-tier limits for any secondary backend.

## Scaling Boundaries

Replica counts after M15 (issue #425):

```text
flick-web: 2
flick-api: 2
flick-worker: 1
postgres: 1
valkey: 1
nats: 1
```

`flick-web` and `flick-api` scale horizontally: neither owns process-local
durable state (databases and NATS are external; the rate limiter and
live-transfer rendezvous live in Valkey). `maxSurge: 0` keeps a node from
scheduling two API replicas. Scaling further requires explicit review:

- A third `flick-api` replica needs node memory headroom or the
  `maxSurge`/placement rules revisited.
- Worker replicas need job receipt idempotency and NATS durable consumer
  behavior checked; one worker collocated with the data tier is the M15
  choice.
- PostgreSQL HA (Patroni or similar), Valkey Sentinel/Cluster, and NATS
  clustering are out of M15 scope against the OCI Always Free memory budget.
