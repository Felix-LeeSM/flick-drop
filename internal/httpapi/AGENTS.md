# `internal/httpapi/` Guide

This package owns HTTP routing, middleware, handlers, and request/response
mapping for public and internal API endpoints.

Directory structure:

- Keep the package flat at first: router, middleware, handlers, DTO mapping, and
  error responses can be separate files.
- If routes split into stable subdomains, add a local `AGENTS.md` before adding
  nested directories.
- Do not place SQL, migrations, NATS stream setup, or object storage SDK logic
  directly in handlers.

Rules:

- `contracts/openapi.yaml` is the public HTTP contract.
- Handlers should translate HTTP into domain calls and map domain results back
  to explicit responses.
- Internal endpoints must require the internal token and must not become a
  general admin API by accident.
- Never accept passphrases or derived keys in API requests.
