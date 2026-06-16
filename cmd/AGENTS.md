# `cmd/` Guide

Each subdirectory is an executable entrypoint.

- `burnlink-api`: starts the public HTTP API and internal API endpoints.
- `burnlink-worker`: consumes NATS JetStream jobs and performs async work.

Keep `cmd/*/main.go` thin. Parse config, wire dependencies, start the process,
and delegate behavior to `internal/` packages.

Do not put domain logic, SQL, NATS stream contracts, or storage policy directly
in `cmd/`.

Service directory contract:

- Every `cmd/<service>/` directory follows the same shape: `main.go`,
  `AGENTS.md`, and only process bootstrap code.
- Do not add `handlers/`, `jobs/`, `storage/`, `migrations/`, `config/`, or
  `utils/` under a service entrypoint.
- If wiring becomes shared or complex, move it into a clearly named
  `internal/*` package rather than creating a service-local framework.
- Service-local files should describe process startup, dependency wiring,
  signal handling, and shutdown only.

Process principles:

- Keep API and worker as separate executables. Do not hide worker loops inside
  the API process unless a later local-dev mode explicitly documents that tradeoff.
- Process startup should fail fast on invalid required config.
- Graceful shutdown should stop HTTP serving, NATS consumption, and database
  use without starting new work.
