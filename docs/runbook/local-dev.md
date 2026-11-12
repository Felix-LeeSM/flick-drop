# Local Development Runbook

## Bootstrap

```sh
mise trust
mise install
direnv allow
cp .env.example .env.local
```

`mise trust` marks the repository's `.mise.toml` as trusted; mise refuses to
load its `[env]` defaults until you do. `.envrc` loads the `.mise.toml`
defaults first and `.env.local` second, so a value in `.env.local` wins.

Edit `.env.local` for local-only secrets such as `FLICK_INTERNAL_TOKEN`. The
default local stack needs no object storage settings: `scripts/dev/up.sh`
uses the local MinIO described below.

## Full Stack

Start NATS, MinIO, API, worker, and the SvelteKit dev server together:

```sh
mise run dev
```

The web UI is available at `http://localhost:5173`, the API at
`http://localhost:8080`, the NATS monitor at `http://localhost:8222`, and the
MinIO S3 API at `http://localhost:9000`.

The dev task reuses an already-running local NATS monitor when
`http://127.0.0.1:8222/varz` is reachable. Otherwise it starts NATS with Docker
Compose and stops that Compose project when the task exits. Press `Ctrl-C` to
stop API, worker, web, and any NATS instance started by the task.

If you want to use a separately managed NATS instance, set
`FLICK_DEV_SKIP_NATS=1` before running the task.

### Local Object Storage (MinIO)

`scripts/dev/up.sh` defaults `FLICK_STORAGE_LARGE_BACKEND` to `s3` and points
the `FLICK_S3_*` variables at MinIO on `http://localhost:9000` with the
`flick-dev` bucket and the `minioadmin` / `minioadmin` local credentials. The
task reuses a MinIO that already answers `http://127.0.0.1:9000/minio/health/live`;
otherwise it starts the `minio` and `createbuckets` services from
`compose.yaml`, and `createbuckets` creates the `flick-dev` bucket. MinIO is
stopped on exit only when the same task also started NATS, because the cleanup
runs `docker compose down` for the whole `flick-dev` Compose project in that
case.

Set `FLICK_DEV_SKIP_MINIO=1` to skip MinIO; the task then sets
`FLICK_STORAGE_LARGE_BACKEND=disabled`, so only payloads up to
`FLICK_PAYLOAD_INLINE_MAX_BYTES` (1 MiB by default) can be created.

Keep `FLICK_STORAGE_LARGE_BACKEND` out of `.env.local` unless you mean to
override the MinIO default: a value set there wins over the `s3` default in
`scripts/dev/up.sh`. `scripts/dev/up.sh` always starts or reuses the local MinIO
and `FLICK_DEV_SKIP_MINIO=1` forces storage off, so the task cannot point at
another store. To test a different S3-compatible store, run `go run ./cmd/flick-api`
yourself with `FLICK_STORAGE_LARGE_BACKEND=s3` and the `FLICK_S3_*` values set.

## Container Images

Build API, worker, and web images locally:

```sh
mise run images
```

See [Container Images](container-images.md) for image names, build arguments,
and runtime defaults.

## NATS

Start local NATS JetStream:

```sh
mise run nats-up
```

Stop it:

```sh
mise run nats-down
```

NATS monitoring is exposed at `http://localhost:8222`.

The API outbox publisher uses:

```text
FLICK_NATS_URL
FLICK_NATS_STREAM
FLICK_NATS_JOB_SUBJECT
```

The publisher sends outbox rows to JetStream but does not carry plaintext,
passphrases, derived keys, or ciphertext bodies.

## API

Run the API service:

```sh
go run ./cmd/flick-api
```

The service listens on `FLICK_API_ADDR` and uses `FLICK_API_DB_PATH`.
Defaults come from `.mise.toml` and local overrides should live in
`.env.local`.

Check the process:

```sh
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

## Data

Local runtime files are under `./var` by default and are ignored by git.

Expected files:

```text
var/api.db
var/worker.db
```
