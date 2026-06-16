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

Directory principles:

- Keep `internal/` one level deep by default. Add nested directories only when a
  package has multiple stable subdomains and add a local `AGENTS.md` with the
  new boundary.
- Do not create `internal/common`, `internal/utils`, or `internal/shared`.
  Choose a package name that states the owned behavior.
- Package names should match durable concepts from `docs/architecture/*`, not
  temporary implementation tactics.
- A package may expose interfaces for dependencies it owns, but it must not use
  interfaces to blur data ownership across API and worker.

Architecture principles:

- Data ownership is stricter than package sharing. Do not add a shared writer
  path because it is convenient.
- API to worker reliability should use the outbox plus NATS JetStream, with
  small ID-based messages.
- NATS payloads carry IDs and safe metadata only. Keep ciphertext in SQLite or
  object storage.
- Prefer direct, readable domain flows over helper indirection in security,
  storage, cleanup, and consume paths.
- Treat `internal/` packages as implementation modules behind documented
  contracts, not as a second service graph.
