# `internal/` Guide

These packages are code boundaries, not service boundaries.

- `config`: environment parsing and validation.
- `db`: SQLite connection setup, migrations, transaction helpers.
- `httpapi`: HTTP routing, handlers, request/response mapping.
- `secrets`: secret lifecycle domain logic.
- `requests`: API-owned request lifecycle, envelope validation, immutable large
  uploads, and request object reconciliation in `internal/requests/store.go`,
  `validation.go`, `large.go`, and `cleanup.go`.
- `storage`: S3-compatible object storage for large payloads, with a primary (`s3_object`) and optional secondary (`r2_object`) backend in `internal/storage/registry.go` (SQLite BLOB lives in `internal/secrets`).
- `piperelay`: the in-memory live relay coordinator behind "send with code" (`/api/pipe/*`); nothing it relays is persisted (`internal/piperelay/coordinator.go:Coordinator`).
- `events`: NATS JetStream publishing and consuming contracts.
- `worker`: worker job execution and retry/idempotency logic.
- `clientcrypto`: client-side encryption for non-browser clients; the Go mirror of `web/src/lib/crypto/`.
- `usage`: daily aggregate usage counters in the api.db `usage_counters` table, written inside secrets and requests state-change transactions (`internal/usage/usage.go:Increment`).
- `flickcli`: the `flick` command-line client's API calls, share links, and send/open flows.
- `telemetry`: structured logging (`log/slog`), Prometheus metrics (counters/gauges served on `/metrics`, guarded by `FLICK_METRICS_TOKEN` bearer auth), and OpenTelemetry tracing (`internal/telemetry/tracing.go:SetupTracing`, on only when `FLICK_OTLP_ENDPOINT` is set).

Service ownership rules:

- `flick-api` owns `api.db`.
- `flick-worker` owns `worker.db`.
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
