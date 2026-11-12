# `internal/httpapi/` Guide

This package owns HTTP routing, middleware, handlers, and request/response
mapping for public and internal API endpoints.

Directory structure:

- Keep the package flat at first: router, middleware, handlers, DTO mapping, and
  error responses can be separate files.
- If routes split into stable subdomains, add a local `AGENTS.md` before adding
  nested directories.
- Handlers must not execute SQL directly, run migrations, set up NATS streams,
  or call object-storage SDK logic. `internal/httpapi/router.go:Server` carries
  the API `*sql.DB` for three uses only: `readyz` pings it, and two secret
  handlers own a transaction boundary that spans two packages —
  `internal/httpapi/secrets.go:Server.openAndCommit` opens a `BeginTx` and
  passes the `*sql.Tx` to `secrets.Store.OpenTx`, and
  `internal/httpapi/secrets.go:Server.enqueueOpenedObjectCleanup` opens one for
  `events.OutboxStore.EnqueueTx`. The handler only begins, commits, and rolls
  back; every query runs inside the domain or outbox method that receives the
  `*sql.Tx`.

Rules:

- `contracts/openapi.yaml` is the public HTTP contract.
- Handlers should translate HTTP into domain calls and map domain results back
  to explicit responses.
- Internal endpoints must require the internal token and must not become a
  general admin API by accident.
- Never accept passphrases or derived keys in API requests.
