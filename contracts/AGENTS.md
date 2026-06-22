# `contracts/` Guide

This directory holds shared service contracts.

Expected files:

- `openapi.yaml`: browser/web to API HTTP contract.
- `credential-payload.schema.json`: browser-only structured credential JSON
  after the `FLCR1:` prefix and before text-secret encryption.
- `events/*.schema.json`: NATS job/event payload contracts.
- Worker→API internal endpoint contract: not yet materialized as a file. The
  internal endpoints live in `internal/httpapi/router.go` (`/internal/...`); add
  `internal-api.md` or an OpenAPI extension when the contract stabilizes.

Contracts must not contain real IDs, credentials, production domains, plaintext
secrets, derived keys, or private bucket names.

Prefer stable, explicit payloads over implicit coupling between Go packages and
Svelte code.

Contract principles:

- Credential payloads are serialized and parsed by the browser only. They are
  encrypted as text secrets before upload, so the API still sees `kind:"text"`
  plus ciphertext and safe metadata only.
- Public secret URLs expose only secret IDs. Contract fields must not imply a
  server-known decryption secret.
- NATS and worker contracts carry IDs, job metadata, reasons, and retry state,
  not encrypted payload bodies.
- Prefer additive, versionable fields over ambiguous overloaded strings.
- When a contract changes, update docs, server mapping, frontend expectations,
  and tests in the same change.
