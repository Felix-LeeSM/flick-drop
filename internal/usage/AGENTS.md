# `internal/usage/` Guide

This package owns the api.db `usage_counters` table: daily aggregate usage
counters written inside the secrets and requests state-change transactions.
There is no HTTP reader — the table is the durable record, inspected through
ops-side DB access; the live scraped view is the Prometheus counters served on
`/metrics` (`internal/telemetry`).

Boundary:

- `usage.Increment` (`usage.go`) is the only writer of `usage_counters`. The
  `internal/secrets` and `internal/requests` stores call it inside the same
  transaction as the state change the counter describes, passing their
  `*sql.Tx` as the `Counter` handle — the single-connection SQLite pool makes
  an out-of-band write deadlock (see `internal/db/storage_usage.go` for the
  same pattern).
- `usage.Store` (`Report`) reads the counters and the live secrets/requests
  inventory for reports. It never writes the domain tables, and no HTTP route
  exposes the report.
- Table schema is created idempotently in `internal/db/migrations.go`
  (`MigrateAPI`), like every other table.

Rules:

- Counter rows carry counts only: no secret or request IDs, no IPs, no user
  agents, no content metadata. A new metric must not add an identifier.
- Deletion and cancellation are not subtracted from counters; they record
  committed usage events. Current inventory comes from the live counts in
  `Store.Report`.
- `usage` must not import `internal/secrets` or `internal/requests` (they
  import `usage`); shared vocabulary such as the 'text'/'file' kind strings is
  pinned by a comment at `usage.go` `secretKindFile`.
