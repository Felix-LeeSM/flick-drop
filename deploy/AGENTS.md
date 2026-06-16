# `deploy/` Guide

Deployment manifests are for generic k3s-compatible self-hosting.

Initial runtime resources:

- `Deployment/burnlink-web`
- `Deployment/burnlink-api`
- `Deployment/burnlink-worker`
- `Service/burnlink-web`
- `Service/burnlink-api`
- `StatefulSet` or `Deployment` for NATS with JetStream file storage
- `PersistentVolumeClaim` for SQLite and NATS data
- `ConfigMap/burnlink-config`
- `Secret/burnlink-secrets`
- `Ingress/burnlink`

Do not commit real secrets, kubeconfig, production bucket names, production
domains, private overlays, or generated PVC/database dumps.

Resource limits matter. The target environment may be OCI Always Free
E2.1.Micro nodes with 1GB RAM each.

Operations principles:

- Keep the baseline deployable without a managed database.
- SQLite and NATS JetStream need persistent volumes; object payload retention
  must match the documented cleanup model.
- OCI Object Storage behavior should be verified with a real development bucket,
  not treated as equivalent to an S3-compatible local simulator.
- Public manifests should be useful to other operators without leaking one
  deployment's tenancy, domain, bucket, or network assumptions.
