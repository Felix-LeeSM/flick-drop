# Implementation Choices

These defaults are selected before code initialization so scaffolded code does
not drift across services.

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
- MinIO is the integration-test double: it is the only proof the manual presigned
  POST signing (`internal/storage/presigned.go`) interoperates with a real S3
  endpoint. The AWS SDK v2 only supports presigned PUT, hence the manual path.

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

Passphrase input is required in the MVP. Share links contain only secret IDs.

Initial KDF:

```text
PBKDF2-HMAC-SHA256
iterations: 600,000 or more
salt: random 128-bit or larger, stored with the secret
derived key: AES-GCM 256-bit key, browser memory only
```

PBKDF2 is selected first because it is available through Web Crypto without a
WASM dependency. Argon2id is the preferred later direction after browser WASM
supply-chain review.

M13 moves payload encryption to age (`filippo.io/age` v1.3.2 in Go,
`age-encryption` 0.3.1 in the browser, both BSD-3-Clause, both by the age
author). age is chosen over a Flick-specific chunked AES-GCM layout because it
is a reviewed, versioned format with two maintained implementations and shared
test vectors, so Flick designs no cryptography. Model A then wraps the
per-secret identity with age's scrypt passphrase recipient (work factor 18)
instead of PBKDF2; the access proof keeps PBKDF2. See
[age format v1](../../contracts/age-format-v1.md).

The server must not receive the passphrase or a value that can directly decrypt
the secret.

The browser also derives a separate access proof with its own KDF salt. The API
stores a hash of that proof and returns ciphertext only from an atomic verified
open operation that marks the secret consumed. Invalid proof attempts increment
a server-side counter without storing passphrases or decrypt keys; the fifth
failed attempt marks the secret consumed and removes its ciphertext payload.
