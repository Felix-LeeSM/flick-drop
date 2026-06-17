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

- BurnLink's UI is browser-crypto heavy and does not require SSR for the MVP.
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
- BurnLink's SQLite usage is straightforward enough to start with the pure-Go
  driver.

If real workload testing exposes compatibility or performance issues, reassess
`mattn/go-sqlite3` with CGO enabled.

## Object Storage

```text
Provider: OCI Object Storage
SDK: OCI Go SDK
Local simulator: none by default
```

Rationale:

- BurnLink targets OCI deployment and should verify behavior against a real OCI
  development bucket.
- MinIO is S3-compatible, not an OCI simulator, so it is not the default test
  double for OCI behavior.

## Containers

Initial preference:

```text
api/worker: small Linux image, Alpine acceptable during early debugging
web: static server image for SvelteKit build output
```

The final runtime image can move toward distroless after the service shape and
debugging needs settle.

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

The server must not receive the passphrase or a value that can directly decrypt
the secret.

The browser also derives a separate access proof with its own KDF salt. The API
stores a hash of that proof and returns ciphertext only from an atomic verified
open operation that marks the secret consumed. Invalid proof attempts increment
a server-side counter without storing passphrases or decrypt keys; the fifth
failed attempt marks the secret consumed and removes its ciphertext payload.
