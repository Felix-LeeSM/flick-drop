# Local Development Runbook

## Bootstrap

```sh
mise install
direnv allow
cp .env.example .env.local
```

Edit `.env.local` for local-only secrets such as `BURNLINK_INTERNAL_TOKEN` and
OCI development bucket settings.

## Full Stack

Start NATS, API, worker, and the SvelteKit dev server together:

```sh
mise run dev
```

The web UI is available at `http://localhost:5173`, the API at
`http://localhost:8080`, and the NATS monitor at `http://localhost:8222`.

The dev task reuses an already-running local NATS monitor when
`http://127.0.0.1:8222/varz` is reachable. Otherwise it starts NATS with Docker
Compose and stops that Compose project when the task exits. Press `Ctrl-C` to
stop API, worker, web, and any NATS instance started by the task.

If you want to use a separately managed NATS instance, set
`BURNLINK_DEV_SKIP_NATS=1` before running the task.

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
BURNLINK_NATS_URL
BURNLINK_NATS_STREAM
BURNLINK_NATS_JOB_SUBJECT
```

The publisher sends outbox rows to JetStream but does not carry plaintext,
passphrases, derived keys, or ciphertext bodies.

## API

Run the API service:

```sh
go run ./cmd/burnlink-api
```

The service listens on `BURNLINK_API_ADDR` and uses `BURNLINK_API_DB_PATH`.
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
