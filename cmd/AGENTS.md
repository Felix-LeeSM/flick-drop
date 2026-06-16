# `cmd/` Guide

Each subdirectory is an executable entrypoint.

- `burnlink-api`: starts the public HTTP API and internal API endpoints.
- `burnlink-worker`: consumes NATS JetStream jobs and performs async work.

Keep `cmd/*/main.go` thin. Parse config, wire dependencies, start the process,
and delegate behavior to `internal/` packages.

Do not put domain logic, SQL, NATS stream contracts, or storage policy directly
in `cmd/`.
