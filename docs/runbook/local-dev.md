# Local Development Runbook

## Bootstrap

```sh
mise install
direnv allow
cp .env.example .env.local
```

Edit `.env.local` for local-only secrets such as `BURNLINK_INTERNAL_TOKEN` and
OCI development bucket settings.

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
