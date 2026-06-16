# `contracts/` Guide

This directory holds shared service contracts.

Expected files:

- `openapi.yaml`: browser/web to API HTTP contract.
- `events/*.schema.json`: NATS job/event payload contracts.
- `internal-api.md` or generated specs for worker to API internal endpoints.

Contracts must not contain real IDs, credentials, production domains, plaintext
secrets, derived keys, or private bucket names.

Prefer stable, explicit payloads over implicit coupling between Go packages and
Svelte code.

Contract principles:

- Public secret URLs expose only secret IDs. Contract fields must not imply a
  server-known decryption secret.
- NATS and worker contracts carry IDs, job metadata, reasons, and retry state,
  not encrypted payload bodies.
- Prefer additive, versionable fields over ambiguous overloaded strings.
- When a contract changes, update docs, server mapping, frontend expectations,
  and tests in the same change.
