# `deploy/` Guide

Deployment manifests are for generic k3s-compatible self-hosting.

Initial runtime resources:

- `Deployment/flick-web`
- `Deployment/flick-api`
- `Deployment/flick-worker`
- `Service/flick-web`
- `Service/flick-api`
- `StatefulSet` or `Deployment` for NATS with JetStream file storage
- `PersistentVolumeClaim` for SQLite and NATS data
- `ConfigMap/flick-config`
- `Secret/flick-secrets`
- `Ingress/flick`

Subdirectories:

- `base/`: public-safe generic k3s manifests with placeholder images and
  placeholder secrets.
- `k3d/`: local smoke-test overlay that must stay disposable and must not carry
  production values.

Do not commit real secrets, kubeconfig, production bucket names, production
domains, private overlays, or generated PVC/database dumps.

Resource limits matter. The target environment may be OCI Always Free
E2.1.Micro nodes with 1GB RAM each.

Operations principles:

- Keep the baseline deployable without a managed database.
- SQLite and NATS JetStream need persistent volumes; object payload retention
  must match the documented cleanup model.
- S3-compatible object storage behavior (OCI Object Storage in
  S3-compatibility mode in prod) should be verified with a real development
  bucket, not treated as equivalent to the local MinIO simulator.
- Public manifests should be useful to other operators without leaking one
  deployment's tenancy, domain, bucket, or network assumptions.
