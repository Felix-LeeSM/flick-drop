# `internal/events/` Guide

This package owns NATS JetStream publishing, consuming, subject names, outbox
integration, and event payload mapping.

Directory structure:

- Keep subjects, payload types, publisher code, consumer setup, and outbox
  bridge code easy to locate.
- Do not create a broad `messages/` or `utils/` subdirectory.
- Contract shape belongs in `contracts/events/*.schema.json`.

Rules:

- NATS payloads contain IDs and safe metadata only.
- Do not publish ciphertext bodies, plaintext, passphrases, derived keys, OCI
  credentials, or private bucket names.
- Publishing from API-owned state should go through the outbox pattern.
- Consumers must be idempotent and safe to replay after partial success.
