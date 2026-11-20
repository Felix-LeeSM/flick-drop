# Runbook

* [Backup and Restore](backup-restore.md) - Hot pg_dump backups, full-read verification, zero-replica database restores, JetStream/Valkey/S3 recovery, and PVC recreation drills.
* [Container Images](container-images.md) - Local builds, multi-arch Docker Hub publishing, web build arguments, and runtime CSP environment defaults for flick-api, flick-worker, and flick-web.
* [k3d Smoke](k3d-smoke.md) - Running the disposable two-node k3d multi-replica smoke test covering cross-pod secrets, live transfer, NetworkPolicies, and rolling restarts.
* [k3s Base Manifests](k3s-base.md) - Validation, image/secret overrides, persistence, and worker-first reconciliation upgrade procedure for deploy/base/.
* [Local Development Runbook](local-dev.md) - Local bootstrap, full dev stack startup, MinIO object storage defaults, mock API cassettes, and parallel worktree stack isolation.
* [OCI Free Tier Deployment](oci-free-tier.md) - Public-safe deployment checklist, private overlay skeleton, S3-compatible OCI/R2 setup, Cloudflare routing, smoke tests, and rollback for OCI Free Tier k3s.
* [PostgreSQL Cutover](postgres-cutover.md) - One-time downtime migration from SQLite volumes to PostgreSQL 17 using scripts/ops/migrate-sqlite-to-postgres.sh with bucket-protection tripwires.
* [PostgreSQL Maintenance](postgres-maintenance.md) - Autovacuum settings, dead-tuple inspection, manual VACUUM and VACUUM FULL trade-offs, WAL disk usage checks, and residual ciphertext hygiene.
* [Storage Backend Rename](storage-backend-rename.md) - One-time upgrade from legacy s3_object/r2_object identifiers and env vars to FLICK_STORAGE_BACKENDS named backends.
* [Worker Cleanup Recovery](worker-recovery.md) - Requeueing previously stranded worker cleanup jobs from flick_api outbox_events within PublishedRetention while preserving idempotency.
