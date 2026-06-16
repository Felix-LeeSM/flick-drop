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

## Data

Local runtime files are under `./var` by default and are ignored by git.

Expected files once the application exists:

```text
var/api.db
var/worker.db
```
