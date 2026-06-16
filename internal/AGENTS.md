# `internal/` Guide

These packages are code boundaries, not service boundaries.

- `config`: environment parsing and validation.
- `db`: SQLite connection setup, migrations, transaction helpers.
- `httpapi`: HTTP routing, handlers, request/response mapping.
- `secrets`: secret lifecycle domain logic.
- `storage`: SQLite BLOB and OCI Object Storage adapters.
- `events`: NATS JetStream publishing and consuming contracts.
- `worker`: worker job execution and retry/idempotency logic.
- `telemetry`: logs, metrics, tracing.

Service ownership rules:

- `burnlink-api` owns `api.db`.
- `burnlink-worker` owns `worker.db`.
- The worker must call internal API endpoints for mutations that belong to
  `api.db`.
- Cross-service actions must be idempotent. Replaying a job after partial
  success must be safe.
