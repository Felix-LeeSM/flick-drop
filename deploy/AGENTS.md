# `deploy/` Guide

Deployment manifests are for generic k3s-compatible self-hosting.

Runtime resources in `deploy/base/kustomization.yaml`:

- `Deployment/flick-web`
- `Deployment/flick-api`
- `Deployment/flick-worker`
- `Service/flick-web`
- `Service/flick-api`
- `Service/flick-worker-metrics` (`deploy/base/worker.yaml`): ClusterIP
  Service for the worker's bearer-guarded `/metrics` listener on port `9091`
- `Service/flick-api-headless` (`deploy/base/api.yaml`, port `8080`) and
  `Service/flick-worker-metrics-headless` (`deploy/base/worker.yaml`, port
  `9091`): headless (`clusterIP: None`) twins whose A records list every pod,
  so `deploy/base/monitoring/vmsingle.yaml` scrapes each replica through
  `dns_sd_configs`
- `StatefulSet/nats` with JetStream file storage, plus `Service/nats` for
  clients and `Service/nats-headless` as the StatefulSet's governing Service
  (`deploy/base/nats.yaml`)
- `Deployment/postgres`, `Service/postgres`, `ConfigMap/postgres-init`, and
  `NetworkPolicy/postgres` (`deploy/base/postgres.yaml`): one PostgreSQL 17
  pod holding the `flick_api` and `flick_worker` databases, each owned by its
  own role; only flick-api and flick-worker pods reach port `5432`
- `Deployment/valkey`, `Service/valkey`, and `NetworkPolicy/valkey`
  (`deploy/base/valkey.yaml`): memory-only Valkey (`--save "" --appendonly no`,
  enforced by `scripts/ci/repo-structure.sh`) reachable from flick-api pods
  only
- `NetworkPolicy/flick-api` (`deploy/base/api.yaml`): the live-transfer stream
  listener (`FLICK_INTERNAL_HTTP_PORT`, `8081`) accepts only other flick-api
  pods; port `8080` stays open to the ingress and the scraper
- `PersistentVolumeClaim/postgres-data` and `PersistentVolumeClaim/nats-data`
  (`deploy/base/pvc.yaml`). flick-api and flick-worker mount no data volume;
  `scripts/ci/repo-structure.sh` rejects a `flick-api-data` or
  `flick-worker-data` claim
- `ConfigMap/flick-config`
- `Secret/flick-secrets` (internal token, metrics token, Valkey key salt),
  `Secret/flick-db-api` and `Secret/flick-db-worker` (one role password each;
  `deploy/base/api.yaml` and `deploy/base/worker.yaml` build
  `FLICK_DATABASE_URL` / `FLICK_WORKER_DATABASE_URL` from their own role's
  password only), and `Secret/flick-db-admin` (postgres superuser)
- `Ingress/flick`
- `ServersTransport/flick-backends` and `Middleware/flick-retry`
  (`deploy/base/traefik.yaml`, Traefik `traefik.io/v1alpha1` CRDs bundled
  with k3s): Traefik opens a fresh connection per request to the
  `Service/flick-api` and `Service/flick-web` pods with a 2 s dial timeout,
  and retries a request that never reached a pod on the other replica. The
  annotations `traefik.ingress.kubernetes.io/service.serverstransport` on
  both Services and `traefik.ingress.kubernetes.io/router.middlewares` on
  `Ingress/flick` reference them as `flick-<name>@kubernetescrd`, so they
  assume namespace `flick`

Replicas and rollout (`deploy/base/api.yaml`, `deploy/base/web.yaml`):
flick-api and flick-web run 2 replicas with `RollingUpdate` `maxSurge: 0`,
`maxUnavailable: 1`, a 5-second `preStop` sleep, and a soft
`topologySpreadConstraints` on `kubernetes.io/hostname`. flick-api also sets
`terminationGracePeriodSeconds: 60` and `FLICK_POD_IP` from the Downward API
(`status.podIP`). The base stays node-agnostic: pinning workloads to specific
nodes or pools belongs in a deployment's private overlay.

Subdirectories:

- `base/`: public-safe generic k3s manifests with placeholder images and
  placeholder secrets.
- `base/monitoring/`: opt-in observability stack (kube-state-metrics,
  node-exporter, VictoriaMetrics single, Loki, Alloy, Grafana), applied
  separately with `kubectl apply -k deploy/base/monitoring` because
  `deploy/base/kustomization.yaml` does not include it. Details live in
  `deploy/base/monitoring/README.md`.
- `k3d/`: local smoke-test overlay that must stay disposable and must not carry
  production values.

Do not commit real secrets, kubeconfig, production bucket names, production
domains, private overlays, or generated PVC/database dumps.

Resource limits matter. The base targets small OCI Always Free-style nodes;
the per-pod requests and limits are recorded in
`docs/architecture/deployment-target.md` (Initial Resource Budget), and a
change to a container's `resources` in `deploy/base/` updates that document.

Operations principles:

- Keep the baseline deployable without a managed database.
- PostgreSQL and NATS JetStream need persistent volumes; Valkey must never get
  one. Object payload retention must match the documented cleanup model.
- S3-compatible object storage behavior (OCI Object Storage in
  S3-compatibility mode in prod) should be verified with a real development
  bucket, not treated as equivalent to the local MinIO simulator.
- Public manifests should be useful to other operators without leaking one
  deployment's tenancy, domain, bucket, or network assumptions.
