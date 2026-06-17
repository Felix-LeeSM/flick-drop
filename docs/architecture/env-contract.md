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
| `BURNLINK_ENV` | `development`, `test`, or `production`. |
| `BURNLINK_LOG_LEVEL` | Log verbosity. |
| `BURNLINK_PUBLIC_BASE_URL` | Public web origin. |
| `PUBLIC_BURNLINK_API_BASE_URL` | Browser-safe API base URL embedded in the web build. |
| `PUBLIC_BURNLINK_LOCAL_FILE_MAX_BYTES` | Browser-safe local file size limit for SQLite-backed file secrets. |
| `BURNLINK_API_BASE_URL` | Public API origin. |
| `BURNLINK_INTERNAL_TOKEN` | Shared token for internal worker to API calls. |
| `BURNLINK_DATA_DIR` | Base directory for local runtime data. |

## API

| Variable | Purpose |
| --- | --- |
| `BURNLINK_API_ADDR` | API listen address. |
| `BURNLINK_API_DB_PATH` | SQLite file owned by API. |
| `BURNLINK_PAYLOAD_INLINE_MAX_BYTES` | Max payload size stored as SQLite BLOB. |
| `BURNLINK_MAX_FILE_BYTES` | Upload hard limit. |
| `BURNLINK_DEFAULT_TTL_SECONDS` | Default expiration. |
| `BURNLINK_ALLOWED_TTL_SECONDS` | Comma-separated allowed TTLs. |
| `BURNLINK_STORAGE_LARGE_BACKEND` | `disabled` or `oci`. |

## NATS

| Variable | Purpose |
| --- | --- |
| `BURNLINK_NATS_URL` | NATS connection URL. |
| `BURNLINK_NATS_STREAM` | JetStream stream name. |
| `BURNLINK_NATS_JOB_SUBJECT` | Job publish subject. |

## Worker

| Variable | Purpose |
| --- | --- |
| `BURNLINK_WORKER_ID` | Stable worker instance label for logs/locks. |
| `BURNLINK_WORKER_DB_PATH` | SQLite file owned by worker. |
| `BURNLINK_WORKER_CONCURRENCY` | Parallel job execution limit. |
| `BURNLINK_INTERNAL_API_BASE_URL` | Internal API base URL. |

## OCI

Used only when `BURNLINK_STORAGE_LARGE_BACKEND=oci`.

| Variable | Purpose |
| --- | --- |
| `BURNLINK_OCI_AUTH_MODE` | `config_file`, `api_key`, or `instance_principal`. |
| `BURNLINK_OCI_CONFIG_FILE` | Local OCI config path for development. |
| `BURNLINK_OCI_PROFILE` | OCI config profile. |
| `BURNLINK_OCI_REGION` | OCI region. |
| `BURNLINK_OCI_NAMESPACE` | Object Storage namespace. |
| `BURNLINK_OCI_BUCKET` | Development or production bucket name. |
| `BURNLINK_OCI_COMPARTMENT_OCID` | Compartment OCID for bucket operations. |
