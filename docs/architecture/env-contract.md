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
| `FLICK_API_BASE_URL` | Public API origin. |
| `FLICK_INTERNAL_TOKEN` | Shared token for internal worker to API calls. |
| `FLICK_DATA_DIR` | Base directory for local runtime data. |

## API

| Variable | Purpose |
| --- | --- |
| `FLICK_API_ADDR` | API listen address. |
| `FLICK_METRICS_TOKEN` | Bearer token guarding `/metrics` on both the API and the worker's dedicated metrics listener. Separate from `FLICK_INTERNAL_TOKEN` (least privilege). Empty = both fail closed (401). |
| `FLICK_WORKER_METRICS_ADDR` | Worker `/metrics` listen address. Default `:9091`; explicitly empty disables the listener. Same bearer credential as the API (`FLICK_METRICS_TOKEN`). |
| `FLICK_API_DB_PATH` | SQLite file owned by API. |
| `FLICK_PAYLOAD_INLINE_MAX_BYTES` | Max age file size stored as SQLite BLOB. Must be at least an empty age file (4128 bytes: `secrets.MaxCiphertextBytes(0)`); smaller values fail startup. `/api/config`, the browser fallback, and the CLI fallback advertise the largest input whose age file fits (`secrets.MaxPlaintextBytes`, 1044208 for the 1 MiB default). |
| `FLICK_MAX_FILE_BYTES` | Upload hard limit in bytes before encryption (the S3 object cap adds the age overhead: `secrets.MaxCiphertextBytes`). While `FLICK_STORAGE_LARGE_BACKEND=disabled` the inline path is the only route, so `/api/config` advertises the inline plaintext limit instead of this value. Raising it also needs the API's `/tmp` `emptyDir` `sizeLimit` (`deploy/base/api.yaml`) to stay at least twice the age size, or a full spool evicts the pod. |
| `FLICK_DEFAULT_TTL_SECONDS` | Default expiration. |
| `FLICK_MIN_TTL_SECONDS` | Minimum secret TTL in seconds. |
| `FLICK_MAX_TTL_SECONDS` | Maximum secret TTL in seconds. |
| `FLICK_STORAGE_LARGE_BACKEND` | `disabled` or `s3`. |
| `FLICK_S3_MAX_TOTAL_BYTES` | Cumulative object-bytes cap per backend for the primary backend (`s3_object`), aggregated from api.db (`db.StorageObjectBytes`); `0` = unlimited. Default `18000000000` (18 GB = OCI Always Free 20 GB × 9/10). Requests and secrets over every backend's remaining headroom fail with `503 storage_full`. The check is check-then-act, so concurrent placements can transiently overshoot a ceiling (soft cap; see storage-model.md). |
| `FLICK_R2_MAX_TOTAL_BYTES` | Same cap for the secondary backend (`r2_object`). Default `9000000000` (9 GB = R2 free 10 GB × 9/10). |
| `FLICK_R2_ENABLED` | `true` enables the secondary backend, used in parallel with the primary to pool free tiers. Independent of `FLICK_STORAGE_LARGE_BACKEND` (requires it to be `s3`; disabled storage disables both). |
| `FLICK_OPEN_RATE_PER_MIN` | Max `/open` requests per client IP + path per minute. |
| `FLICK_CREATE_RATE_PER_MIN` | Max `/api/secrets` (presigned upload issuance) requests per client IP + path per minute. |
| `FLICK_REAPER_INTERVAL_SECONDS` | Seconds between expiration-reaper sweeps of expired/orphan secrets in api.db. |
| `FLICK_REAPER_BATCH_SIZE` | Max secrets reclaimed per reaper tick. |
| `FLICK_PIPE_RELAY_ENABLED` | Enable live in-memory pipe relay endpoints (`true`/`false`). Default `true`. |
| `FLICK_PIPE_MAX_CONCURRENT` | Maximum concurrent live pipe rendezvous sessions. Default `100`. |
| `FLICK_TRUSTED_PROXIES` | Comma-separated CIDRs whose peer may set X-Forwarded-For. Empty = direct peer IP only. |

## NATS

| Variable | Purpose |
| --- | --- |
| `FLICK_NATS_URL` | NATS connection URL. |
| `FLICK_NATS_STREAM` | JetStream stream name. |
| `FLICK_NATS_JOB_SUBJECT` | Job publish subject. |

## Worker

| Variable | Purpose |
| --- | --- |
| `FLICK_WORKER_ID` | Stable worker instance label for logs/locks. |
| `FLICK_WORKER_DB_PATH` | SQLite file owned by worker. |
| `FLICK_WORKER_CONCURRENCY` | Parallel job execution limit. |
| `FLICK_INTERNAL_API_BASE_URL` | Internal API base URL. |

## Web

The optional public-guide sponsor is build-time repository configuration in
`web/src/lib/components/sponsor.ts`, not an environment variable or remote
configuration endpoint. `SPONSOR = null` disables it. See the configuration and
measurement boundaries in [advertising-experiment.md](advertising-experiment.md).

Runtime env for the nginx-served web container (distinct from the build-time
`PUBLIC_FLICK_*` vars baked into the bundle). Read at container start, not build.

| Variable | Purpose |
| --- | --- |
| `FLICK_CSP_CONNECT_SRC` | Extra space-separated origins appended to the web CSP `connect-src` (beyond `'self'`). Empty by default — correct when the large-file S3 backend is disabled. When `FLICK_STORAGE_LARGE_BACKEND=s3`, set the `FLICK_S3_ENDPOINT` origin (`scheme://host[:port]`); when `FLICK_R2_ENABLED=true` is also set, list both backend origins so the browser's direct presigned uploads pass CSP. Rendered into nginx by the envsubst entrypoint. |

## S3

Used only when `FLICK_STORAGE_LARGE_BACKEND=s3`. The server speaks the S3 API
directly via the AWS SDK, so any S3-compatible provider works (MinIO dev, OCI
S3-compat prod, AWS S3, R2). Auth is always a static key pair — OCI Customer
Secret Key in prod — because the AWS SDK cannot speak OCI instance principal.

| Variable | Purpose |
| --- | --- |
| `FLICK_S3_ENDPOINT` | S3-compatible endpoint. Empty = AWS default host. MinIO `http://localhost:9000`; OCI `https://<namespace>.compat.objectstorage.<region>.oraclecloud.com`. |
| `FLICK_S3_REGION` | Region (`us-east-1` for MinIO; real region for OCI/AWS). |
| `FLICK_S3_BUCKET` | Bucket name. |
| `FLICK_S3_ACCESS_KEY_ID` | Static access key ID (MinIO `minioadmin`; OCI Customer Secret Key). |
| `FLICK_S3_SECRET_ACCESS_KEY` | Static secret access key. |
| `FLICK_S3_PATH_STYLE` | `true` (default) for MinIO/OCI path-style; `false` for virtual-host. |

The API's existing S3 credentials also require `ListObjectsV2` permission for the
exclusive `managed/secrets/` and `managed/requests/` prefixes. Request finalize
requires API GET of the staging key and server-only PUT of the verified bytes to
the final key; requester open requires GET of the final key. The API therefore
needs GetObject/PutObject in addition to signed upload and listing permissions.
The worker retains its existing object-delete
permission. No new environment variable or startup permission probe is introduced:
listing failures are reported by the reaper and retried without disabling existing
S3 upload/open operation. Provider versions, retention locks, replicas, and backups
remain operator-owned; listing only reconciles current objects.

## R2 (secondary backend)

Used only when `FLICK_R2_ENABLED=true` and the primary backend (`FLICK_STORAGE_LARGE_BACKEND=s3`) is enabled. New objects land on the primary first and spill to the secondary once the primary's remaining ceiling cannot fit (`storage.ChooseBackend`); existing objects never move. Auth is an R2 API token (static key pair); region stays `auto`.

| Variable | Purpose |
| --- | --- |
| `FLICK_R2_ENDPOINT` | R2 S3 endpoint, `https://<account>.r2.cloudflarestorage.com`. |
| `FLICK_R2_REGION` | Region literal; `auto` (default) is the R2 value. |
| `FLICK_R2_BUCKET` | Secondary bucket name. |
| `FLICK_R2_ACCESS_KEY_ID` | R2 API token access key. |
| `FLICK_R2_SECRET_ACCESS_KEY` | R2 API token secret. |
| `FLICK_R2_PATH_STYLE` | `true` (default) — R2 accepts path-style. |

The secondary needs the same permission set as the primary (signed PUT for browser uploads, GET for finalize copy and opens, PUT for finalize, DELETE, ListObjectsV2 for reconciliation). Delete jobs carry the backend name in the `backend` field of the NATS payload (event-contract.md); the worker needs both backends' credentials. R2 has no hard spending cap (budget alerts are notification-only), which is why the code-side ceiling is the enforcement layer.
