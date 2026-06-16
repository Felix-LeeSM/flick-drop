# `deploy/` Guide

Deployment manifests are for generic k3s practice.

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
