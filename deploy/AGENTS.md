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
- `StatefulSet/nats` with JetStream file storage, plus `Service/nats` for
  clients and `Service/nats-headless` as the StatefulSet's governing Service
  (`deploy/base/nats.yaml`)
- `PersistentVolumeClaim/flick-api-data`, `PersistentVolumeClaim/flick-worker-data`,
  and `PersistentVolumeClaim/nats-data` (`deploy/base/pvc.yaml`)
- `ConfigMap/flick-config`
- `Secret/flick-secrets`
- `Ingress/flick`

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
- SQLite and NATS JetStream need persistent volumes; object payload retention
  must match the documented cleanup model.
- S3-compatible object storage behavior (OCI Object Storage in
  S3-compatibility mode in prod) should be verified with a real development
  bucket, not treated as equivalent to the local MinIO simulator.
- Public manifests should be useful to other operators without leaking one
  deployment's tenancy, domain, bucket, or network assumptions.
