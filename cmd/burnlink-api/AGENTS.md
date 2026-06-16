# `cmd/burnlink-api/` Guide

This directory is the `burnlink-api` process entrypoint.

Directory structure:

- `main.go`: parse config, wire dependencies, start HTTP serving, handle
  shutdown.
- `AGENTS.md`: local process rules.
- Do not add subdirectories here.

Keep handlers, middleware, request/response mapping, domain behavior, SQL,
migrations, storage adapters, NATS publishing, and telemetry implementation in
named `internal/*` packages.

Startup wiring should stay easy to read:

1. load config
2. open API-owned SQLite
3. wire storage, secrets, events, and telemetry
4. build HTTP router
5. start server and graceful shutdown

The API process is the only writer for `api.db`.
