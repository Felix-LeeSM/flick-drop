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
| `FLICK_PUBLIC_BASE_URL` | Public web origin. |
| `PUBLIC_FLICK_API_BASE_URL` | Browser-safe API base URL embedded in the web build. |
| `PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES` | Browser-safe local file size limit for SQLite-backed file secrets. |
| `FLICK_API_BASE_URL` | Public API origin. |
| `FLICK_INTERNAL_TOKEN` | Shared token for internal worker to API calls. |
| `FLICK_DATA_DIR` | Base directory for local runtime data. |

## API

| Variable | Purpose |
| --- | --- |
| `FLICK_API_ADDR` | API listen address. |
| `FLICK_API_DB_PATH` | SQLite file owned by API. |
| `FLICK_PAYLOAD_INLINE_MAX_BYTES` | Max payload size stored as SQLite BLOB. |
| `FLICK_MAX_FILE_BYTES` | Upload hard limit. |
| `FLICK_DEFAULT_TTL_SECONDS` | Default expiration. |
| `FLICK_MIN_TTL_SECONDS` | Minimum secret TTL in seconds. |
| `FLICK_MAX_TTL_SECONDS` | Maximum secret TTL in seconds. |
| `FLICK_STORAGE_LARGE_BACKEND` | `disabled` or `s3`. |
| `FLICK_OPEN_RATE_PER_MIN` | Max `/open` requests per client IP + path per minute. |
| `FLICK_CREATE_RATE_PER_MIN` | Max `/api/secrets` (presigned POST issuance) requests per client IP + path per minute. |
| `FLICK_REAPER_INTERVAL_SECONDS` | Seconds between expiration-reaper sweeps of expired/orphan secrets in api.db. |
| `FLICK_REAPER_BATCH_SIZE` | Max secrets reclaimed per reaper tick. |
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
