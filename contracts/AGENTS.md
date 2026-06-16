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
