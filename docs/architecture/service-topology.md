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

`internal/requests` implements the independent inline request lifecycle behind
`/api/requests`; the API alone writes `requests` and `request_payloads`. Existing
send-secret routes and worker ownership are unchanged.

The API also publishes async jobs through an outbox table and NATS JetStream.

`internal/piperelay` implements the live relay ("Send with code") behind
`/api/pipe/*` (`internal/httpapi/piperelay.go`). Its sessions live only in the
memory of one API process: nothing reaches `api.db`, NATS, the worker, or
object storage, and a restart drops every open session. Because the sender's
long-poll (`/wait`), the receivers' `/join`, `/status`, and `/receive`, and the
stream (`/send`) must all reach the process that holds the session, the relay
depends on the single API replica in `deploy/base/api.yaml` (`replicas: 1`);
scaling the API out would first need session-sticky routing by code. No relay
request stays silent longer than 25 seconds before a decision
(`internal/httpapi/piperelay.go` `pipeWaitMaxHold`), under the 100-second idle
cut of a proxy such as Cloudflare. The flow:

1. Sender `POST /api/pipe/init` gets the 6-digit code, salt, and sender token.
2. Each receiver `POST /api/pipe/join` with the code gets a connection ID (and,
   with confirmation on, its own 3-digit number).
3. With confirmation on, each receiver long-polls `GET /api/pipe/status` (up to
   25 seconds per poll) until it is selected, declined, not selected, or the
   session ends. A receiver with no open `/status` call for 35 seconds
   (`internal/piperelay/coordinator.go` `defaultPresenceTimeout`) drops out of
   the room. With confirmation off, the receiver opens `GET /api/pipe/receive`
   right after `/join`.
4. Sender long-polls `GET /api/pipe/wait?since=N` (up to 25 seconds per poll)
   for the waiting list or, with confirmation off, for the first receiver.
5. With confirmation on the sender calls `POST /api/pipe/select` or
   `POST /api/pipe/decline` per connection; `POST /api/pipe/cancel` burns the
   session before streaming. Only the selected receiver then opens
   `GET /api/pipe/receive`; it must do so within 35 seconds or the session
   ends.
6. Sender `POST /api/pipe/send` streams into the chosen receiver's `/receive`.

`docs/architecture/security-model.md` (Live Relay) defines what each credential
unlocks and why rejected receivers never burn a session.

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
