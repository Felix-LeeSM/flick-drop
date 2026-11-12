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

## Backend HTTP

```text
Language: Go
Router: net/http + chi
```

Rationale:

- `net/http` keeps the server close to the Go standard library.
- `chi` adds lightweight path parameters and middleware without a large
  framework surface.

## SQLite

```text
Driver: modernc.org/sqlite
CGO: disabled by default
```

Rationale:

- CGO-free builds simplify local development, CI, cross-compilation, and small
  container images.
- Flick's SQLite usage is straightforward enough to start with the pure-Go
  driver.

If real workload testing exposes compatibility or performance issues, reassess
`mattn/go-sqlite3` with CGO enabled.

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
- Backends are named and parallel: the primary (`s3_object`) and an optional
  secondary (`r2_object`) resolved through `storage.Registry`, so free-tier
  capacity pools without moving existing objects. Placement honors per-backend
  byte ceilings (`storage.ChooseBackend`); see `storage-model.md`.
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
data, run as a non-root `flick` user, and use `/data` for SQLite files. The web
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
