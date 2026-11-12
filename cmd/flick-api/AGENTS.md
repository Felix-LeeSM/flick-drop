# `cmd/flick-api/` Guide

This directory is the `flick-api` process entrypoint.

Directory structure:

- `main.go`: parse config, wire dependencies, start HTTP serving, handle
  shutdown.
- `AGENTS.md`: local process rules.
- Do not add subdirectories here.

Keep handlers, middleware, request/response mapping, domain behavior, SQL,
migrations, storage adapters, NATS publishing, and telemetry implementation in
named `internal/*` packages. (Structured logging, Prometheus metrics on
`/metrics`, and OpenTelemetry tracing are implemented in `internal/telemetry`;
`cmd/flick-api/main.go` calls `telemetry.SetupTracing`, which exports spans only
when `FLICK_OTLP_ENDPOINT` is set.)

Startup wiring should stay easy to read:

1. load config
2. open API-owned SQLite
3. wire storage, secrets, and events (metrics are process-global instruments
   in `internal/telemetry/metrics.go`; tracing is installed by
   `telemetry.SetupTracing` right after config load in `cmd/flick-api/main.go`)
4. build HTTP router
5. start server and graceful shutdown

The API process is the only writer for `api.db`.
