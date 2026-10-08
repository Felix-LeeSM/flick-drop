# `internal/requests/` Guide

- `store.go` owns the API-only inline request lifecycle in `requests` and
  `request_payloads`; `internal/db/migrations.go:MigrateAPI` owns the schema.
- `validation.go` enforces `docs/architecture/request-links.md`: canonical RSA
  SPKI, independent 32-byte capabilities, and the version 1 encrypted envelope.
- `Store.Submit`, `Store.Open`, and `Store.Revoke` serialize transitions in
  SQLite transactions. `Store.Open` and `Store.Revoke` remove inline bytes in
  the same transaction; no request ciphertext enters NATS or the worker.
- `PurgeExpiredTx` removes request metadata and cascading payloads at the original
  deadline. `internal/secrets/reaper.go:ClaimOnce` schedules the bounded purge.
- HTTP handlers live in `internal/httpapi/requests.go`. Never log request
  bodies, tokens, public keys, encrypted filenames, or ciphertext.
- Large reservations and object storage remain outside `internal/requests`
  until #207 implements the approved generation and cleanup contract.
