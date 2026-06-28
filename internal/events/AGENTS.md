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
- `JobEvent.TraceContext` carries the W3C trace context of the enqueuing span,
  injected by `OutboxStore.EnqueueTx` (while that span is still live — the async
  publisher runs after it ends) and re-established by `ContextWithTrace` so
  `worker.Process` continues the producer's trace (#133). It is telemetry IDs
  only (traceparent/baggage) — never secret content — and is absent when tracing
  is off. Propagation rides the payload, not NATS headers, so no header plumbing
  or interface change is needed.
