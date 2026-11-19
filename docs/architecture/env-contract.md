---
type: Architecture
title: Environment Contract
description: Public contract for all runtime, build-time, CLI, PostgreSQL, Valkey, NATS, and named S3-compatible storage environment variables.
tags: [architecture, env, config]
status: stable
---

# Environment Contract

All runtime configuration is supplied by environment variables.

Local development:

- `.mise.toml` pins tools and provides safe defaults.
- `.envrc` loads mise output and then `.env.local`.
- `.env.local` is private and untracked.
- `.env.example` is the public contract.

## Common

| Variable | Purpose |
| --- | --- |
| `FLICK_ENV` | `development`, `test`, or `production`. |
| `FLICK_LOG_LEVEL` | Log verbosity (`debug`/`info`/`warn`/`error`; default `info`). |
| `FLICK_LOG_FORMAT` | Log output format (`json` default, `text` for local dev). |
| `FLICK_OTLP_ENDPOINT` | OpenTelemetry OTLP/HTTP collector URL for distributed tracing (full URL incl. scheme, e.g. `http://otel-collector:4318`). A URL without a path exports to `/v1/traces`; an explicit path, including `/`, is preserved. Empty (default) disables tracing — no collector dependency, zero overhead. Applies to both api and worker. |
| `FLICK_PUBLIC_BASE_URL` | Public web origin. |
| `PUBLIC_FLICK_API_BASE_URL` | Browser-safe API base URL embedded in the web build. |
| `PUBLIC_FLICK_MIN_TTL_SECONDS` | Browser-safe mirror of `FLICK_MIN_TTL_SECONDS` (client validation). Default must equal the Go const; `scripts/ci/ttl-drift.sh` enforces it. |
| `PUBLIC_FLICK_MAX_TTL_SECONDS` | Browser-safe mirror of `FLICK_MAX_TTL_SECONDS` (client validation). Default must equal the Go const; `scripts/ci/ttl-drift.sh` enforces it. |
| `PUBLIC_FLICK_DEFAULT_TTL_SECONDS` | Browser-safe mirror of `FLICK_DEFAULT_TTL_SECONDS` (initial lifetime preset). Default must equal the Go const; `scripts/ci/ttl-drift.sh` enforces it. |
| `FLICK_API_BASE_URL` | API origin used by local scripts only: `scripts/dev/up.sh` polls `${FLICK_API_BASE_URL}/healthz` and `scripts/smoke/local-secret-flow.sh` sends its requests there. No Go service reads it; the browser uses `PUBLIC_FLICK_API_BASE_URL`. |
| `FLICK_INTERNAL_TOKEN` | Shared token for internal worker to API calls, and for the API replicas' live-transfer stream hop (`internal/piperelay/stream.go` `InternalTokenHeader`). |

## API

| Variable | Purpose |
| --- | --- |
| `FLICK_API_ADDR` | API listen address. |
| `FLICK_METRICS_TOKEN` | Bearer token guarding `/metrics` on both the API and the worker's dedicated metrics listener. Separate from `FLICK_INTERNAL_TOKEN` (least privilege). Empty = both fail closed (401). |
| `FLICK_DATABASE_URL` | PostgreSQL DSN for the API-owned `flick_api` logical database, connected as the least-privilege `flick_api` role (`pgx/v5/stdlib`, `CGO_ENABLED=0`). Required since the M15 PR 3 cutover: the API fails fast without it and has no local file fallback. Local dev defaults match `compose.yaml` + `scripts/dev/postgres-init.sql` (`scripts/dev/up.sh` exports them). In Kubernetes `deploy/base/api.yaml` builds it from `Secret/flick-db-api` `password` (URL-safe), so the API pod never holds the worker's credentials. |
| `FLICK_VALKEY_ADDR` | Valkey `host:port` for the shared rate limiter and live-transfer rendezvous (`internal/httpapi/ratelimit.go`, `internal/piperelay/`). Empty disables Valkey use: the rate limiter falls back to per-process memory and `/api/pipe/*` answers `503`. |
| `FLICK_RATELIMIT_KEY_SALT` | Deployment-secret salt mixed (HMAC-SHA-256) into Valkey rate-limit keys and live-transfer room keys and client hashes, so Valkey never stores a raw client network or a 6-digit code. Required when `FLICK_VALKEY_ADDR` is set; every replica must share it, and rotating it resets in-flight rate-limit buckets and orphans live-transfer rooms (at most 3 minutes of sessions). |
| `FLICK_INTERNAL_HTTP_PORT` | Port of the API's live-transfer stream listener (`/internal/pipe/stream/{roomID}`, `internal/piperelay/stream.go`), the same on every replica: a sender's replica dials the receiver's replica on it. Default `8081`; must be 1..65535. The listener runs whenever live transfer is enabled (`cmd/flick-api/main.go`), also outside Kubernetes; it binds only the `FLICK_POD_IP` address (`127.0.0.1` outside Kubernetes), accepts only requests carrying `FLICK_INTERNAL_TOKEN` and a stream's one-time secret, and must stay unreachable from outside the cluster or host. |
| `FLICK_POD_IP` | IP literal this replica advertises in Valkey for its stream listener, injected by the Kubernetes Downward API (`status.podIP`). Empty (one host outside Kubernetes) advertises `127.0.0.1`; a host name fails startup. |
| `FLICK_PAYLOAD_INLINE_MAX_BYTES` | Max age file size stored inline as `secret_payloads.ciphertext` (`bytea`). Must be at least an empty age file (4128 bytes: `secrets.MaxCiphertextBytes(0)`); smaller values fail startup. `/api/config`, the browser fallback, and the CLI fallback advertise the largest input whose age file fits (`secrets.MaxPlaintextBytes`, 1044208 for the 1 MiB default). |
| `FLICK_MAX_FILE_BYTES` | Upload hard limit in bytes before encryption (the S3 object cap adds the age overhead: `secrets.MaxCiphertextBytes`). While `FLICK_STORAGE_BACKENDS` is empty the inline path is the only route, so `/api/config` advertises the inline plaintext limit instead of this value. Raising it also needs the API's `/tmp` `emptyDir` `sizeLimit` (`deploy/base/api.yaml`) to stay at least twice the age size, or a full spool evicts the pod. |
| `FLICK_DEFAULT_TTL_SECONDS` | Default expiration. |
| `FLICK_MIN_TTL_SECONDS` | Minimum secret TTL in seconds. |
| `FLICK_MAX_TTL_SECONDS` | Maximum secret TTL in seconds. |
| `FLICK_STORAGE_BACKENDS` | Comma-separated object backend names, for example `oci,r2`. Empty (default) disables object storage: every payload stays inline. Each name is a lower-case letter followed by up to 15 lower-case letters or digits (`storage.ValidBackendName`) and is configured by the `FLICK_STORAGE_<NAME>_*` variables in [Object storage backends](#object-storage-backends). |
| `FLICK_OPEN_RATE_PER_MIN` | Per-minute limit per client network (IPv4 address or IPv6 /64) for `/api/secrets/{id}/open` and sender management (one bucket per secret ID), for all request-link calls after creation (one bucket across IDs), and for `/api/pipe/join` (one bucket shared with `/api/v1/pipe/join`). |
| `FLICK_CREATE_RATE_PER_MIN` | Per-minute limit per client network (IPv4 address or IPv6 /64) for `POST /api/secrets` (includes presigned upload issuance), `POST /api/requests`, and `/api/pipe/init`, each with its own bucket. |
| `FLICK_REAPER_INTERVAL_SECONDS` | Seconds between expiration-reaper sweeps of expired/orphan secrets in the `flick_api` database. |
| `FLICK_REAPER_BATCH_SIZE` | Max secrets reclaimed per reaper tick. |
| `FLICK_PIPE_RELAY_ENABLED` | Enable live pipe relay endpoints (`true`/`false`). Default `true`. Live transfer also needs `FLICK_VALKEY_ADDR` (and therefore `FLICK_RATELIMIT_KEY_SALT`, which keys its Valkey rooms) and a non-empty `FLICK_INTERNAL_TOKEN`; without Valkey the API logs that live transfer is disabled and `/api/pipe/*` answers `503`. |
| `FLICK_PIPE_MAX_CONCURRENT` | Maximum concurrent live pipe rendezvous sessions, counted across every API replica in Valkey. Default `100`. |
| `FLICK_TRUSTED_PROXIES` | Comma-separated CIDRs whose peer may set X-Forwarded-For. Empty = direct peer IP only. |

## NATS

| Variable | Purpose |
| --- | --- |
| `FLICK_NATS_URL` | NATS connection URL. |
| `FLICK_NATS_STREAM` | JetStream stream name. |
| `FLICK_NATS_JOB_SUBJECT` | Job publish subject. |

## PostgreSQL

The M15 architecture runs one PostgreSQL 17 instance (`postgres:17-alpine`) and
splits two logical databases with separate least-privilege roles, so the API and
worker keep their ownership boundary (`docs/architecture/database-schema.md`):

```text
flick_api database    owner role flick_api    <- FLICK_DATABASE_URL
flick_worker database owner role flick_worker <- FLICK_WORKER_DATABASE_URL
```

Local development mirrors this with `compose.yaml` and
`scripts/dev/postgres-init.sql`. CI (`scripts/ci/go.sh` integration tests) reads
`FLICK_TEST_POSTGRES_URL` instead: a test URL whose schemas the test helpers
create and drop per run.

## Valkey

Valkey (`valkey/valkey:8.1-alpine`) is the non-persistent shared state tier,
always run with `--save "" --appendonly no` so nothing reaches disk. It holds
only rate-limit buckets (HMAC-SHA-256 hashed keys, TTL ≤ 10 minutes) and
live-transfer rendezvous metadata (room state, Pub/Sub signals, poll leases);
ciphertext never enters Valkey (`security-model.md`). A Valkey outage never
fails `/readyz` (which checks Postgres and NATS only): the rate limiter falls
back to per-process memory and only `/api/pipe/*` answers `503`. CI reads
`FLICK_TEST_VALKEY_ADDR` (`host:port`) for integration tests.

## Worker

| Variable | Purpose |
| --- | --- |
| `FLICK_WORKER_DB_PATH` | Retired by the M15 PostgreSQL cutover (PR 2/#421, PR 3/#422): no code reads it. Superseded by `FLICK_WORKER_DATABASE_URL`. |
| `FLICK_WORKER_DATABASE_URL` | PostgreSQL DSN for the worker-owned `flick_worker` logical database, connected as the least-privilege `flick_worker` role. Required since PR 2; the worker fails fast without it. Local dev defaults match `compose.yaml` + `scripts/dev/postgres-init.sql` (`scripts/dev/up.sh` exports them). In Kubernetes `deploy/base/worker.yaml` builds it from `Secret/flick-db-worker` `password` (URL-safe). |
| `FLICK_INTERNAL_API_BASE_URL` | Internal API base URL. |
| `FLICK_WORKER_METRICS_ADDR` | Worker `/metrics` listen address (`internal/config/config.go`). Default `:9091`; explicitly empty disables the listener. Same bearer credential as the API (`FLICK_METRICS_TOKEN`). |

## Web

The optional public-guide sponsor is build-time repository configuration in
`web/src/lib/components/sponsor.ts`, not an environment variable or remote
configuration endpoint. `SPONSOR = null` disables it. See the configuration and
measurement boundaries in [advertising-experiment.md](advertising-experiment.md).

Runtime env for the nginx-served web container (distinct from the build-time
`PUBLIC_FLICK_*` vars baked into the bundle). Read at container start, not build.

| Variable | Purpose |
| --- | --- |
| `FLICK_CSP_CONNECT_SRC` | Extra space-separated origins appended to the web CSP `connect-src` (beyond `'self'`). Empty by default — correct when `FLICK_STORAGE_BACKENDS` is empty. When object storage is enabled, list every backend's `FLICK_STORAGE_<NAME>_ENDPOINT` origin (`scheme://host[:port]`) so the browser's direct presigned uploads pass CSP. Rendered into nginx by the envsubst entrypoint. |

## CLI

Read by the `flick` command-line client (`cmd/flick/main.go`) on the user's
machine, not by any server process.

| Variable | Purpose |
| --- | --- |
| `FLICK_URL` | Deployment URL for `flick send`, and for `flick open` when it is given a bare secret ID. `flick open` with a full link uses that link's origin (`cmd/flick/main.go` `runOpen`). The `-url` flag overrides both; when neither is set the CLI uses its built-in default. |
| `FLICK_PASSPHRASE` | Passphrase for non-interactive use instead of a terminal prompt. Set but empty is an error. The passphrase stays on the client and is never sent to the API. |

## Object storage backends

Used only when `FLICK_STORAGE_BACKENDS` names at least one backend. The server
speaks the S3 API directly via the AWS SDK, so any S3-compatible provider works
(MinIO, OCI S3-compat, AWS S3, Cloudflare R2). Auth is always a static key pair
— an OCI Customer Secret Key or an R2 API token — because the AWS SDK cannot
speak OCI instance principal.

Each name `<name>` in `FLICK_STORAGE_BACKENDS` reads the variables below, with
`<NAME>` the upper-cased name (`oci` reads `FLICK_STORAGE_OCI_BUCKET`, `minio`
reads `FLICK_STORAGE_MINIO_BUCKET`). Parsing and validation live in
`internal/config/config.go:loadStorageBackends`; a missing required value fails
startup.

| Variable | Purpose |
| --- | --- |
| `FLICK_STORAGE_<NAME>_ENDPOINT` | S3-compatible endpoint. Empty = AWS default host. MinIO `http://localhost:9000`; OCI `https://<namespace>.compat.objectstorage.<region>.oraclecloud.com`; R2 `https://<account>.r2.cloudflarestorage.com`. |
| `FLICK_STORAGE_<NAME>_REGION` | Region. Default `us-east-1` (MinIO); the real region for OCI/AWS; `auto` for R2. |
| `FLICK_STORAGE_<NAME>_BUCKET` | Bucket name. Required. |
| `FLICK_STORAGE_<NAME>_ACCESS_KEY_ID` | Static access key ID (MinIO `minioadmin`; OCI Customer Secret Key; R2 API token). Required. |
| `FLICK_STORAGE_<NAME>_SECRET_ACCESS_KEY` | Static secret access key. Required. |
| `FLICK_STORAGE_<NAME>_PATH_STYLE` | `true` (default) for path-style addressing (MinIO, OCI, R2); `false` for virtual-host. |
| `FLICK_STORAGE_<NAME>_MAX_TOTAL_BYTES` | Cumulative object-bytes ceiling for this backend, aggregated from the `flick_api` database (`db.StorageObjectBytes`). Default `0` = unlimited, allowed only when exactly one backend is configured; with several backends every ceiling must be positive. New objects go to a backend that still fits them, chosen at random weighted by remaining space (`storage.Registry.Choose`); when none fits, creation fails with `503 storage_full`. The check is check-then-act, so concurrent placements can transiently overshoot (soft cap; see `storage-model.md`). Set it below the provider's hard limit, for example `18000000000` (90 percent of OCI Always Free 20 GB) or `9000000000` (90 percent of R2 free 10 GB). |

The backend name is stored in every object row's `storage_backend` column and in
delete jobs' `backend` field (`event-contract.md`), so it must stay stable while
objects exist on that backend. Removing a name that still holds objects makes
opens, cleanup, and reconciliation of those objects fail instead of reaching the
wrong bucket.

The variables of releases before `FLICK_STORAGE_BACKENDS`
(`FLICK_STORAGE_LARGE_BACKEND`, `FLICK_S3_*`, `FLICK_R2_*`) are rejected: flick-api
and flick-worker refuse to start while any of them is set to a non-empty value
(`internal/config/config.go:legacyStorageEnv`). The
[storage backend rename runbook](../runbook/storage-backend-rename.md) maps each
one to its replacement.

Every backend's credentials need the same permissions. The API signs browser
PUTs, needs GetObject (finalize copy of the staging key, opens) and PutObject
(server-only finalize write of the verified bytes), and `ListObjectsV2` on the
exclusive `managed/secrets/` and `managed/requests/` prefixes for
reconciliation. The worker needs DeleteObject on every backend, so it receives
every backend's credentials. No startup permission probe is introduced: listing
failures are reported by the reaper and retried without disabling upload/open.
Provider versions, retention locks, replicas, and backups remain operator-owned;
listing only reconciles current objects. Providers without a hard spending cap
(R2 budget alerts are notification-only) rely on
`FLICK_STORAGE_<NAME>_MAX_TOTAL_BYTES` as the enforcement layer.

