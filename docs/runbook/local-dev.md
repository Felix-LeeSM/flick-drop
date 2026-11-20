---
type: Runbook
title: Local Development Runbook
description: Local bootstrap, full dev stack startup, MinIO object storage defaults, mock API cassettes, and parallel worktree stack isolation.
tags: [runbook, local-dev, minio, mock-api]
status: stable
---

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

`scripts/dev/up.sh` defaults `FLICK_STORAGE_BACKENDS` to one backend named
`minio` and points the `FLICK_STORAGE_MINIO_*` variables at MinIO on `http://localhost:9000` with the
`flick-dev` bucket and the `minioadmin` / `minioadmin` local credentials. The
task reuses a MinIO that already answers `http://127.0.0.1:9000/minio/health/live`;
otherwise it starts the `minio` and `createbuckets` services from
`compose.yaml`, and `createbuckets` creates the `flick-dev` bucket. MinIO is
stopped on exit only when the same task also started NATS, because the cleanup
runs `docker compose down` for the whole `flick-dev` Compose project in that
case.

CI's MinIO storage integration (`scripts/ci/storage-integration.sh`) layers a
temporary Compose file over the root `compose.yaml` that swaps in the image
built from `scripts/ci/Dockerfile.minio` and keeps its own
`entrypoint: ["minio"]` and `command: ["server", "/data"]`, because the root
`minio` service (`bitnamilegacy/minio:latest`) sets neither and the CI image
would otherwise start with no arguments and exit. When you change the root
MinIO service, keep that overlay self-contained.

Set `FLICK_DEV_SKIP_MINIO=1` to skip MinIO; the task then empties
`FLICK_STORAGE_BACKENDS`, so only payloads up to
`FLICK_PAYLOAD_INLINE_MAX_BYTES` (1 MiB by default) can be created.

Keep `FLICK_STORAGE_BACKENDS` out of `.env.local` unless you mean to
override the MinIO default: a value set there, including an empty one, wins
over the `minio` default in `scripts/dev/up.sh`. Also ensure no legacy storage
variables (`FLICK_S3_*`, `FLICK_R2_*`, `FLICK_STORAGE_LARGE_BACKEND`) remain
exported in your shell or `.env.local`: `internal/config/config.go:loadStorageBackends`
refuses to boot `flick-api` and `flick-worker` while any legacy variable is
non-empty.

`scripts/dev/up.sh` always starts or reuses the local MinIO and
`FLICK_DEV_SKIP_MINIO=1` forces storage off, so the task cannot point at
another store. To test a different S3-compatible store, run `go run ./cmd/flick-api`
yourself with `FLICK_STORAGE_BACKENDS` and that backend's
`FLICK_STORAGE_<NAME>_*` values set.

When running live browser suites repeatedly against `scripts/dev/up.sh`, the
default `FLICK_CREATE_RATE_PER_MIN=5` can trigger `429` timeouts on the second
run. Start the stack with higher limits (and `FLICK_MIN_TTL_SECONDS=1` for
request expiry tests):

```sh
FLICK_CREATE_RATE_PER_MIN=100 FLICK_OPEN_RATE_PER_MIN=100 FLICK_MIN_TTL_SECONDS=1 ./scripts/dev/up.sh
```

## Mock API for Prototypes

`scripts/dev/mock-api.mjs` records real API responses once and replays them
without the API, NATS, or MinIO running, including large uploads and send with
code. Use it to show a UI prototype in a
fixed state (created link, first open, already opened) without recreating that
state by hand.

Record against a running API (`http://localhost:8080` by default,
`FLICK_MOCK_TARGET` overrides it), then point a web dev server at the mock
(`http://localhost:8090` by default, `FLICK_MOCK_PORT` overrides it):

```sh
node scripts/dev/mock-api.mjs record text-open
PUBLIC_FLICK_API_BASE_URL=http://localhost:8090 pnpm --dir web dev
```

Walk through the flow in the browser. Every response is appended to
`var/mock-api/text-open.json`. The browser keeps decryption keys in the URL
fragment, which never reaches the mock, so to replay an open page later paste
the full recipient link into the cassette's `link` field while recording; the
recorder keeps it. Then stop the API and replay:

```sh
node scripts/dev/mock-api.mjs replay text-open
```

Replay matches requests by method and path, ignoring the query string. When the
same request was recorded several times, the mock returns the recordings in
order and then repeats the last one, so a recorded "first open 200, then 404"
plays back the same way. Requests with no recording return `501` with
`mock_no_recording`. Replay moves every JSON `*_at` timestamp and the
`Flick-Expires-At` header forward by the time since recording, so a cassette
recorded with a one-hour TTL still shows one hour left days later instead of
an expired management page.

- `POST http://localhost:8090/__mock/reset` restarts every sequence from the
  first recording.
- `POST http://localhost:8090/__mock/use/<name>` switches to `<name>.json` in
  the cassette directory (`var/mock-api/`, or `FLICK_MOCK_DIR`) in replay mode.

Recording refuses a cassette name that already exists; delete the file first to
record it again. Logs show only the path of the cassette's `link`, never its
`#key=` fragment.

Browser tests replay committed cassettes from `tests/fixtures/cassettes/`
through `routeCassette` in `scripts/dev/mock-api.mjs`, which answers `/api/*`
requests in a Playwright page or context; see
`docs/architecture/ci-testing.md`. Record those against a throwaway local
stack with synthetic content only:

```sh
FLICK_MOCK_DIR=tests/fixtures/cassettes node scripts/dev/mock-api.mjs record model-b-text-open-once
```

Limits:

- Cassettes contain management tokens and ciphertext. `var/` is in
  `.gitignore`; keep prototype cassettes out of the repository. Commit only
  test cassettes recorded with synthetic content against a local database you
  delete afterwards.
- Every recorded response replays, failures included (404, 410, 413, 429,
  503 from the API). A failure that never produces a response — a dropped
  connection or a timeout — cannot be recorded; browser tests use
  Playwright `route.abort()` for those.
- Live relay timing replays only in recorded order: a long-poll
  (`/api/pipe/wait`, `/api/pipe/status`, `/api/pipe/receive`) answers once
  every `/api/pipe/*` request recorded before it has replayed, or after 30
  seconds when the browser polls a different number of times than the
  recording did. Streamed bodies replay whole.

Large uploads: while recording, the mock rewrites every presigned `url` in a
JSON response to `http://localhost:8090/__storage/<base64url of the original>`,
so the browser's object storage `PUT` also goes through the mock, reaches the
real bucket, and replays later without MinIO. `routeCassette` answers those
`/__storage/` requests too.

Live transfer (`/api/pipe/*`): the receiving browser makes a new age key on
every attempt, so recorded ciphertext cannot be opened on replay. While
recording, write the synthetic plaintext you sent into the cassette's `pipe`
field — `{"text": "…"}`, plus `"filename"` when you sent a file. On replay the
mock takes the new recipient from the `/api/pipe/join` request and encrypts
that plaintext (and filename) to it for `/api/pipe/receive`. The sending side
replays as recorded.

`node --test scripts/dev/mock-api.test.mjs` checks the record-then-replay round
trip against a stub upstream, the timestamp shift, the storage rewrite, the
live relay re-encryption, the long-poll order, and that logs omit the link key.

## Prototype Folders

UI prototypes live in folders under `web/src/routes/prototype/` that are listed
one by one in `.git/info/exclude`. Add a new folder to `.git/info/exclude`
before creating it; otherwise git tracks it.

- The local index `http://localhost:5173/prototype/` comes from the
  git-excluded `web/src/routes/prototype/+page.ts`, which lists each
  `prototype/<slug>/` from its `README.md`: the `# ` title, the first plain
  paragraph before the first `## ` as the summary, and the first
  `/prototype/<slug>?…` address as the start link. Give every new prototype
  README those three.
- Tailwind v4 source detection skips git-excluded paths, so a class used only
  in a prototype folder produces no CSS. Add a temporary
  `@source "./prototype/<slug>/**";` to `web/src/routes/layout.css` (remove it
  with the folder) or use scoped `<style>`; `web/AGENTS.md` lists the class-name
  collisions to avoid.
- Draw focus outlines for text inputs on the padded, rounded outer box with
  `:has(input:focus-visible)`, and exclude inputs from a root `:focus-visible`
  outline rule, so an outline never hugs the typed text.
- A `position: relative` card with a large `box-shadow` paints over a
  following unpositioned sibling (a button row turns grey); give that sibling
  `position: relative` too.

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

The service listens on `FLICK_API_ADDR` and connects to
`FLICK_DATABASE_URL` (the `flick_api` PostgreSQL database — `scripts/dev/up.sh`
starts the compose Postgres and exports both DSNs). Local overrides live in
`.env.local`.

Check the process:

```sh
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
```

## Data

Local state lives in the compose PostgreSQL instance (docker volumes, not the
working tree). `scripts/dev/up.sh` starts it and pins the two DSNs, so no
database files appear under `./var` any more.

## Running a Second Stack in a Worktree

When another session already holds the primary checkout (ports `8080` and
`5173`), never kill its processes. Use `git worktree add` and isolate every
endpoint and backing resource:

- **Single CORS origin:** `cmd/flick-api/main.go` allows only
  `FLICK_PUBLIC_BASE_URL` (default `http://localhost:5173`). `localhost` and
  `127.0.0.1` are distinct origins. If the second web dev server uses another
  port (for example `http://127.0.0.1:5199`), start a second `flick-api` with
  `FLICK_PUBLIC_BASE_URL=http://127.0.0.1:5199` and pass its address via
  `PUBLIC_FLICK_API_BASE_URL`.
- **NATS is required at API boot:** `flick-api` exits if `FLICK_NATS_URL` is
  unreachable. Either reuse the running NATS or start a separate container on
  another port with `--jetstream`.
- **Isolated database and storage:** Point `FLICK_DATABASE_URL` at a dedicated
  PostgreSQL schema (`search_path=...`). A second `flick-worker` also needs its
  own `FLICK_WORKER_DATABASE_URL`: `scripts/dev/up.sh` defaults it to the shared
  `flick_worker` database, and `cmd/flick-worker/main.go` runs
  `db.MigrateWorker` on that database at boot. Set `FLICK_STORAGE_BACKENDS=` when
  testing inline-only flows without MinIO. Clean up only the containers and
  schema created by the worktree session.
