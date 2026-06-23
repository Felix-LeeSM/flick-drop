# Service Topology

Flick starts as a small self-hosted MSA.

## Runtime Services

```text
Browser
  -> flick-web
  -> flick-api
  -> NATS JetStream
  -> flick-worker
```

### `flick-web`

SvelteKit frontend. It handles browser-side encryption and decryption. It may be
served as static assets or a small Node runtime, depending on the selected
SvelteKit adapter.

### `flick-api`

Public HTTP/JSON API for secret creation, metadata lookup, and verified one-time
open operations. It is the only service that mutates `api.db` directly.

The API also publishes async jobs through an outbox table and NATS JetStream.

### `flick-worker`

Long-running worker Deployment. It consumes NATS JetStream jobs and performs
cleanup, S3 object deletion, retry bookkeeping, and future backup/restore
verification work.

The worker owns `worker.db`. For changes that belong to `api.db`, it calls
internal API endpoints on `flick-api`.

### `nats`

NATS with JetStream file storage. It carries job IDs and small metadata only.
Messages must not contain plaintext, passphrases, derived keys, or ciphertext
bodies.

## Communication

- Browser to web/API: HTTP.
- API to worker: NATS JetStream jobs.
- Worker to API: internal HTTP endpoints protected by `FLICK_INTERNAL_TOKEN`.
- API/worker to object storage: AWS SDK for Go v2 over S3-compatible endpoints
  (MinIO dev, OCI S3-compatibility prod) when enabled.

Do not add gRPC in the initial version. It is a future option if internal HTTP
contracts become too wide or streaming RPC becomes useful.

## Reliability

Use the outbox pattern for API-published jobs:

1. API commits the domain change and an `outbox_events` row in `api.db`.
2. API outbox publisher sends the event to NATS JetStream.
3. API marks the outbox row as sent after publish ack.
4. Worker consumes the job and performs idempotent work.

Every worker job must tolerate duplicate delivery and partial prior success.
