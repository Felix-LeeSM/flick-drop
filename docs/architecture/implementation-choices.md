---
type: Architecture
title: Implementation Choices
description: Implementation defaults for SvelteKit, i18n, Go HTTP/chi, PostgreSQL 17, Valkey, AWS SDK v2 S3-compatible storage, containers, IDs, and age/PBKDF2 crypto.
tags: [architecture, choices, stack]
status: stable
---

# Implementation Choices

These are the implementation defaults every Flick service follows, recorded so
the services do not drift apart.

## Frontend

```text
Framework: SvelteKit
Adapter: @sveltejs/adapter-static
Package manager: pnpm
```

Rationale:

- Flick's UI is browser-crypto heavy and does not require SSR for the MVP.
- Static output keeps the web service small and easy to deploy.
- Runtime secrets must not be exposed to the frontend; only `PUBLIC_` values are
  allowed in browser code.

If SSR-only needs appear later, the adapter choice can be revisited.

### Supported browsers

The web build uses Tailwind CSS 4 (`web/package.json`), whose output relies on
CSS `@property` and `color-mix()`. Supported browsers are Safari 16.4+,
Chrome 111+ (and Chromium-based Edge), and Firefox 128+. Older browsers,
often an outdated iOS in-app browser, render a half-styled page.
`web/src/app.html` runs an inline ES5 check (`window.CSSPropertyRule` and
`CSS.supports('color', 'color-mix(in srgb, red, blue)')`) before the app loads
and, when either is missing, inserts a `role="alert"` notice asking the reader
to update the browser before opening a secret. The check opens or consumes
nothing.

### Display language

The web app shows English, Korean, and Japanese. `web/src/lib/i18n/` holds typed
message catalogs (`messages/en/*.ts` is the source; `ko` and `ja` are typed as the
same shape, so a missing key fails `pnpm check`) and the current language
(`locale.svelte.ts`). No i18n library is used: three languages and one catalog per
screen area need only typed objects and the browser's `Intl`.

- The language never appears in a URL, so a shared link opens in the recipient's
  own language. A manual choice from the header globe menu beside the theme
  toggle (`web/src/lib/components/LanguageRow.svelte` in
  `web/src/lib/components/SiteHeader.svelte`) is stored in `localStorage`
  (`flick-locale`); otherwise the first supported browser language wins, then
  English.
- Pages are prerendered in English. The language is applied after hydration
  (`web/src/routes/+layout.svelte`), so a Korean or Japanese reader briefly sees
  English on a cold load.
- Korean and Japanese use Pretendard / Pretendard JP for body text and Noto Serif
  KR / JP for serif headings, self-hosted from npm (`pretendard`, `pretendard-jp`,
  `@fontsource-variable/noto-serif-kr`, `@fontsource-variable/noto-serif-jp`) and
  selected by `<html lang>` in `web/src/routes/layout.css`. Every face is split by
  `unicode-range`, so English pages download none of them.
- Server error messages are not shown; the web client maps error codes to its own
  catalog text (`web/src/lib/api/secrets.ts`). The API, worker, and `flick` CLI stay
  English; the one server string the web shows, the waiting device's name ("Safari on
  iPhone" from `internal/httpapi/pipedevice.go:pipeDeviceLabel`), is reworded per
  language by `web/src/lib/i18n/device.ts:deviceLabelText`.
- The `/guides/` prose is one component per language
  (`web/src/routes/guides/Guide{En,Ko,Ja}.svelte`); its headings, table of contents,
  and page title come from `messages/*/guides.ts` so they stay in step.
- Tests compare wording by catalog key; only `web/src/lib/i18n/catalogs.test.ts`
  walks every language, checking that each has every `en` key with the same shape.

## Backend HTTP

```text
Language: Go
Router: net/http + chi
```

Rationale:

- `net/http` keeps the server close to the Go standard library.
- `chi` adds lightweight path parameters and middleware without a large
  framework surface.

## PostgreSQL

```text
Driver: github.com/jackc/pgx/v5/stdlib (database/sql interface)
Dialect: PostgreSQL 17, single dialect
CGO: disabled
```

Rationale (decided 2026-11-16, milestone M15):

- One database dialect instead of a `?`/`$N` rebinding dual dialect: the
  SQLite-specific SQL (`julianday`, `datetime`, `pragma`, boolean addition)
  has no portable translation, and per-dialect code paths cost more than they
  save at Flick's scale.
- `pgx/v5/stdlib` keeps the `database/sql` interface and pure-Go builds
  (`CGO_ENABLED=0`), so local development, CI, cross-compilation, and small
  images keep working with no C toolchain.
- Multi-replica correctness rides Postgres features SQLite lacks:
  session-level `pg_advisory_lock` around migrations, `RETURNING id` instead
  of `LastInsertId`, and `FOR UPDATE SKIP LOCKED` claims for the outbox,
  reaper, and request cleanup. See `database-schema.md`.

M15 shipped the move in order: `cmd/flick-worker` and its store moved to
Postgres first (issue #421), then the API-owned database and the
`modernc.org/sqlite` removal (issue #422). Existing self-hosted deployments
migrate once through the SQLite cutover runbook
(`docs/runbook/postgres-cutover.md`, issue #425), which reads the old files
with the `sqlite3` CLI (`scripts/ops/migrate-sqlite-to-postgres.sh`) rather
than a Go SQLite driver.

## Valkey

```text
Client: github.com/redis/go-redis/v9 (pure Go; v9.22 — v9.23 requires Go 1.26)
Persistence: none (`--save "" --appendonly no`)
```

Rationale (decided 2026-11-16, milestone M15):

- `replicas >= 2` needs state that outlives one API process only for the rate
  limiter and live-transfer rendezvous; both are ephemeral by design, so a
  non-persistent Valkey carries them without a new durability surface.
- Valkey stores hashed keys and small metadata only — never ciphertext
  (`security-model.md`, "Valkey") — and a Valkey outage degrades the rate
  limiter to per-process memory plus a `503` on `/api/pipe/*` rather than
  failing `/readyz`.
- Live-transfer rooms live in Valkey since M15 PR 5 (issue #424); the
  ciphertext goes replica-to-replica over plain HTTP on
  `FLICK_INTERNAL_HTTP_PORT` instead of through Valkey, because Valkey Streams
  would buffer a whole 256 MiB transfer in a small Valkey and a reverse proxy to
  one owning replica would tie the waiting room to that replica's lifetime.

## Object Storage

```text
Provider: S3-compatible (MinIO dev, OCI S3-compatibility prod, AWS S3, R2 portable)
SDK: AWS SDK for Go v2
Local test double: MinIO (internal/storage/minio_integration_test.go)
```

Rationale:

- Large payloads are written through the S3 API directly via the AWS SDK for Go
  v2, so any S3-compatible provider works — MinIO dev, OCI S3-compatibility prod,
  AWS S3, Cloudflare R2. See `internal/storage/AGENTS.md` and `env-contract.md`.
- Backends are operator-named and parallel (`FLICK_STORAGE_BACKENDS`, for
  example `oci,r2`) and resolved by name through `storage.Registry`, so
  free-tier capacity pools without moving existing objects. No backend is
  primary: placement picks among backends whose byte ceiling still fits the
  object, weighted by remaining space (`storage.Registry.Choose`); see
  `storage-model.md`.
- Browser uploads use a presigned PUT from the SDK's `PresignPutObject`
  (`internal/storage/presigned.go:PresignPUT`). The signature covers
  `Content-Length`, so the bucket rejects any body that is not exactly the
  declared age file length. Flick uses PUT rather than presigned POST because
  OCI's S3 Compatibility API does not implement POST Object.
- MinIO is the integration-test double: it proves the presigned PUT and
  signed `Content-Length` interoperate with a real S3 endpoint.

## Containers

Current definitions:

```text
api: Dockerfile.api, static Go binary on Alpine
worker: Dockerfile.worker, static Go binary on Alpine
web: web/Dockerfile, SvelteKit static output served by nginx
```

The Go images keep `CGO_ENABLED=0`, install only CA certificates and timezone
data, and run as a non-root `flick` user with no data volume — state lives in
PostgreSQL. The web
image builds with pinned pnpm through Corepack and serves the static adapter
output on port 8080 as the unprivileged `nginx` user.

The web image defaults `PUBLIC_FLICK_API_BASE_URL=/`, so Kubernetes can route
API traffic through the same public origin. Deployments may override that build
argument when a separate API origin is intentional.

## IDs

Secret IDs and job IDs must be random, URL-safe, and not guessable.

Initial target:

```text
128 bits or more of randomness
base62/base64url style encoding
no sequential IDs for public secret URLs
```

UUIDs are acceptable internally, but public share IDs should stay short and URL
friendly.

## Passphrase KDF

A passphrase is optional. Model A (passphrase) secrets derive an access proof
and wrap the age identity with the passphrase; Model B (link-key) secrets carry
a random key in the URL fragment (`#key=...`) and store no access KDF or proof
hash (`docs/architecture/security-model.md`, "Model B — Passphrase Optional").
The share link path and query contain only the secret ID in both models.

The access proof KDF below applies to Model A only.

Access proof KDF:

```text
PBKDF2-HMAC-SHA256
iterations: 600,000 to 6,000,000 (the cap bounds what a stored block can make an opener compute)
salt: random 128-bit or larger, stored with the secret
output: 256-bit proof; the server keeps only its hash
```

PBKDF2 is selected because it is available through Web Crypto without a WASM
dependency.

Payload encryption is age (`filippo.io/age` v1.3.2 in Go,
`age-encryption` 0.3.1 in the browser, both BSD-3-Clause, both by the age
author). age is chosen over a Flick-specific chunked AES-GCM layout because it
is a reviewed, versioned format with two maintained implementations and shared
test vectors, so Flick designs no cryptography. Model A wraps the
per-secret identity with age's scrypt passphrase recipient (work factor 18);
the access proof keeps PBKDF2. See
[age format v1](../../contracts/age-format-v1.md).

The server must not receive the passphrase or a value that can directly decrypt
the secret.

For Model A the browser also derives a separate access proof with its own KDF
salt. The API stores a hash of that proof and returns ciphertext only from an
atomic verified open operation that deletes the `secrets` row, its inline
payload, and its management record in one transaction
(`internal/httpapi/secrets.go:openAndCommit`). Invalid proof attempts increment
a server-side counter without storing passphrases or decrypt keys; the fifth
failed attempt marks the secret consumed and removes its ciphertext payload.
