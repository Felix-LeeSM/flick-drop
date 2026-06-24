# `cmd/flick-worker/` Guide

This directory is the `flick-worker` process entrypoint.

Directory structure:

- `main.go`: parse config, wire dependencies, start NATS consumption, handle
  shutdown.
- `AGENTS.md`: local process rules.
- Do not add subdirectories here.

Keep job execution, idempotency, retry policy, NATS contracts, worker SQLite
state, internal API calls, and telemetry implementation in named `internal/*`
packages. (Structured logging and Prometheus metrics are implemented in
`internal/telemetry`; tracing is planned — see #94 phase 3.)

Startup wiring should stay easy to read:

1. load config
2. open worker-owned SQLite
3. connect to NATS JetStream
4. wire job handlers and internal API client
5. start consumers and graceful shutdown

The worker process is the only writer for `worker.db`. It must not mutate
`api.db` directly.
