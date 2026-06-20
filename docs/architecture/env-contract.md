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
| `FLICK_LOG_LEVEL` | Log verbosity. |
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
| `FLICK_STORAGE_LARGE_BACKEND` | `disabled` or `oci`. |
| `FLICK_OPEN_RATE_PER_MIN` | Max `/open` requests per client IP + path per minute. |
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

## OCI

Used only when `FLICK_STORAGE_LARGE_BACKEND=oci`.

| Variable | Purpose |
| --- | --- |
| `FLICK_OCI_AUTH_MODE` | `config_file`, `api_key`, or `instance_principal`. |
| `FLICK_OCI_CONFIG_FILE` | Local OCI config path for development. |
| `FLICK_OCI_PROFILE` | OCI config profile. |
| `FLICK_OCI_REGION` | OCI region. |
| `FLICK_OCI_NAMESPACE` | Object Storage namespace. |
| `FLICK_OCI_BUCKET` | Development or production bucket name. |
| `FLICK_OCI_COMPARTMENT_OCID` | Compartment OCID for bucket operations. |
