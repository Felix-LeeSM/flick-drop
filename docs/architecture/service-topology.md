---
type: Architecture
title: Service Topology
description: Runtime services (flick-web, flick-api, flick-worker, nats, postgres, valkey), live-relay pod-to-pod flow, and outbox reliability.
tags: [architecture, topology, services]
status: stable
---

# Service Topology

Flick starts as a small self-hosted MSA.

## Runtime Services

```text
Browser
  -> flick-web
  -> flick-api
  -> PostgreSQL (flick_api / flick_worker logical databases)
  -> NATS JetStream
  -> flick-worker
  -> Valkey (shared rate limiter + live-transfer rendezvous)
```

### `flick-web`

SvelteKit frontend. It handles browser-side encryption and decryption. It is
built with `@sveltejs/adapter-static` (`web/vite.config.ts`, fallback
`200.html`) and served as static assets by nginx (`web/Dockerfile`); there is
no Node runtime in production.

### `flick-api`

Public HTTP/JSON API for secret creation, metadata lookup, and verified one-time
open operations. It is the only service that mutates the `flick_api` database
directly.

`internal/requests` implements the independent inline request lifecycle behind
`/api/requests`; the API alone writes `requests` and `request_payloads`. Existing
send-secret routes and worker ownership are unchanged.

The API also publishes async jobs through an outbox table and NATS JetStream.

The API runs with `replicas: 2` (M15, issue #422 onward): `flick-api` owns no
process-local durable state — the databases and NATS are external, and the
rate limiter plus live-transfer rendezvous live in Valkey — so a
`RollingUpdate` with `maxSurge: 0, maxUnavailable: 1` serves traffic
continuously. On `SIGTERM` the API wakes the waiting-room long-polls
(`/wait`, `/status`) so clients reconnect to the surviving replica, and drains
in-flight transfers inside `terminationGracePeriodSeconds: 60`
(`preStop: sleep 5`, server `Shutdown` timeout 50s).

`internal/piperelay` implements the live relay ("Live transfer") behind
`/api/pipe/*` (`internal/httpapi/piperelay.go`), so any API replica answers any
call of a transfer. Each room's whole state is one Valkey hash
(`pipe:room:{roomID}`, where `roomID` is the HMAC-SHA-256 of the 6-digit code
under `FLICK_RATELIMIT_KEY_SALT`), written by compare-and-set Lua scripts that
announce every change on `pipe:ev:{roomID}` (`internal/piperelay/room.go`).
Each replica holds one Pub/Sub pattern subscription and wakes its local
long-polls from it, re-reading the room at least every 2 seconds
(`resyncInterval`) in case a message was lost. `pipe:poll:{roomID}:{hash of the
connection ID}` marks a waiting receiver present and expires 35 seconds after
its last `/status` poll started or ended. `pipe:polls:{roomID}:{hash of the
connection ID}` holds one member per open poll, scored by its 25-second hold
plus those 35 seconds on Valkey's clock, so a poll that a dead replica never
released stops counting by then on its own. `pipe:rooms` (sorted by deadline) backs the concurrent-session
cap across replicas. Ciphertext bytes never touch Valkey.

The encrypted stream itself goes pod-to-pod over direct HTTP
(`internal/piperelay/stream.go`). The receiver's `/receive` writes its
replica's `FLICK_POD_IP` into the room and waits; the sender's replica draws a
one-time stream secret, stores only its SHA-256 in the room, and dials
`POST http://{receiver IP}:{FLICK_INTERNAL_HTTP_PORT}/internal/pipe/stream/{roomID}`
with `X-Flick-Internal-Token` and the secret. The dial target is an IP literal
from the room plus this deployment's fixed internal port, never a host name, and
the client never follows a redirect or an `HTTP_PROXY`. The stream listener
answers `200` only after the whole body reached the receiver's response, so the
sender's replica tells a completed transfer from a receiver that left
(`502 receiver_disconnected`). Nothing reaches the databases, NATS, the worker,
or object storage. A waiting room survives the replica that created it: a
rolling restart cuts only a stream still running when the 50-second shutdown
timeout ends. No relay request stays silent longer than 25 seconds before a
decision (`internal/httpapi/piperelay.go` `pipeWaitMaxHold`), under the
100-second idle cut of a proxy such as Cloudflare. The flow:

1. Sender `POST /api/pipe/init` gets the 6-digit code and sender token.
2. Each receiver `POST /api/pipe/join` with the code and its browser-generated
   age recipient gets a connection ID and its own random 3-digit confirmation
   number.
3. Each receiver long-polls `GET /api/pipe/status` (up to 25 seconds per poll)
   until it is selected, declined, not selected, or the session ends. A receiver
   with no open `/status` call for 35 seconds (`internal/piperelay/coordinator.go`
   `defaultPresenceTimeout`) drops out of the room. On `SIGTERM` an open poll
   answers at once (`Coordinator.Drain`), and the browser re-polls through
   another replica (`web/src/lib/crypto/pipe.ts` `fetchLongPoll` retries one
   5xx or dropped socket).
4. Sender long-polls `GET /api/pipe/wait?since=N` (up to 25 seconds per poll)
   for the waiting list (each row carries that receiver's age recipient).
5. Sender calls `POST /api/pipe/select` or `POST /api/pipe/decline` per
   connection; `POST /api/pipe/cancel` burns the session before streaming. Only
   the selected receiver then opens `GET /api/pipe/receive`; it must do so
   within 35 seconds or the session ends.
6. Sender `POST /api/pipe/send` streams an age file encrypted to the chosen
   receiver's recipient; whichever pod holds the sender dials the receiver
   pod's internal stream listener, which writes the bytes into that receiver's
   open `/receive` response.

`docs/architecture/live-relay.md` defines what each credential
unlocks and why rejected receivers never burn a session.

`FLICK_PIPE_RELAY_ENABLED=false` or an empty `FLICK_VALKEY_ADDR`
(`internal/config/config.go` `Load`) starts the API without a coordinator or
stream listener (`cmd/flick-api/main.go`): `/api/config` reports
`pipe_relay_enabled: false` and every `/api/pipe/*` call answers
`503 service_unavailable`. A Valkey error at runtime answers the same `503` for
that call only. The web UI still shows "Live transfer";
a send or receive attempt then reads "Live transfer is not available on this
server right now." (`web/src/lib/crypto/pipe.ts`), wording that fits both
cases.

The relay is a server pipe rather than WebRTC peer-to-peer on purpose. Direct
browser-to-browser connections fail behind many NATs and mobile carrier CGNAT
unless a TURN relay is also run, so a TURN server would carry the bytes anyway;
an HTTP pipe through `flick-api` works wherever the site itself loads and adds
no extra service to operate.

### `flick-worker`

Long-running worker Deployment. It consumes NATS JetStream jobs and performs
cleanup, S3 object deletion, retry bookkeeping, and future backup/restore
verification work.

The worker owns the `flick_worker` database. For changes that belong to the
`flick_api` database, it calls internal API endpoints on `flick-api`. The
worker stays a single replica collocated with the data tier (Osaka
`flick-core`): a second replica would add cross-region latency to NATS,
Postgres, and S3 for no throughput gain at Flick's scale, and its job lease
logic (`FOR UPDATE SKIP LOCKED` claims, `ON CONFLICT` receipts, M15 issue
#421) is what keeps even one replica restart-safe.

### `nats`

NATS with JetStream file storage. It carries job IDs and small metadata only.
Messages must not contain plaintext, passphrases, derived keys, or ciphertext
bodies.

### `postgres`

One PostgreSQL 17 instance (`postgres:17-alpine`) on `flick-core` owning both
logical databases (`flick_api`, `flick_worker`) with separate least-privilege
roles (`database-schema.md`). It is deliberately single-instance: multi-node
HA (Patroni or similar) is out of M15 scope against the OCI Always Free
memory budget, so the API's `/readyz` and the cutover runbook treat it as the
one stateful dependency to watch. Migrations serialize through a session-level
`pg_advisory_lock`, so two API replicas booting concurrently cannot corrupt
catalog state.

### `valkey`

Valkey 8 (`valkey/valkey:8.1-alpine`) on `flick-core`, run non-persistent
(`--save "" --appendonly no`). It stores rate-limit buckets (HMAC-hashed keys,
TTL ≤ 10 minutes) and live-transfer room metadata, Pub/Sub signals, and poll
leases — hashed keys and small metadata only, never ciphertext
(`security-model.md`, "Valkey"). Losing Valkey degrades the rate limiter to
per-process memory and answers `/api/pipe/*` with `503`; `/readyz` keeps
passing because it checks Postgres and NATS only.

## Communication

- Browser to web/API: HTTP.
- API to worker: NATS JetStream jobs.
- Worker to API: internal HTTP endpoints protected by `FLICK_INTERNAL_TOKEN`.
- API to Postgres: `FLICK_DATABASE_URL` (`pgx/v5/stdlib`).
- Worker to Postgres: `FLICK_WORKER_DATABASE_URL` (`pgx/v5/stdlib`).
- API to Valkey: `FLICK_VALKEY_ADDR` for rate limiting and live-transfer
  rendezvous; pod-to-pod live-transfer streams ride direct HTTP between
  `POD_IP` addresses on `FLICK_INTERNAL_HTTP_PORT` and never traverse Valkey.
- API/worker to object storage: AWS SDK for Go v2 over S3-compatible endpoints
  when enabled. `FLICK_STORAGE_BACKENDS` names one or more backends, each
  configured by `FLICK_STORAGE_<NAME>_*` ([env contract](env-contract.md)):
  one MinIO backend (`minio`) in development, and for example OCI
  S3-compatibility (`oci`) plus Cloudflare R2 (`r2`) in production. The API
  places each new object on a backend with room, weighted by remaining space,
  and records the name on the row; the worker resolves the delete target from
  the job event's `backend` field ([event contract](event-contract.md)). API
  and worker must configure the same backend names.

Do not add gRPC in the initial version. It is a future option if internal HTTP
contracts become too wide or streaming RPC becomes useful.

## Reliability

Use the outbox pattern for API-published jobs:

1. API commits the domain change and an `outbox_events` row in the `flick_api` database.
2. API outbox publisher sends the event to NATS JetStream.
3. API marks the outbox row as sent after publish ack.
4. Worker consumes the job and performs idempotent work.

Every worker job must tolerate duplicate delivery and partial prior success.
