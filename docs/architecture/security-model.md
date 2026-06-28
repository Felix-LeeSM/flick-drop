# Security Model

Flick is designed around client-side encryption and short retention.

## Security Goal

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

The browser derives an encryption key — from a user-entered passphrase in
Model A, or from a random key in Model B — and encrypts payloads before
upload. The API, worker, NATS, SQLite, S3-compatible object storage, logs, and
metrics handle ciphertext and safe metadata only.

The browser also derives a separate access proof from the same user input with
separate KDF parameters. The API stores only a hash of that proof. The proof
cannot decrypt the payload; it only gates the one-time open operation.

## Assets

Protected assets:

- plaintext text secrets
- plaintext file contents
- passphrases
- derived encryption keys
- plaintext filenames
- private deployment credentials
- admin/internal tokens

## Trust Boundaries

```text
Browser
  owns plaintext, passphrase, and derived key

flick-web
  serves UI and public client config only

flick-api
  stores ciphertext, metadata, and access proof hashes
  owns api.db
  publishes outbox-backed jobs

flick-worker
  consumes jobs
  owns worker.db
  calls internal API endpoints

NATS JetStream
  stores job IDs and small metadata only

S3-compatible object storage
  stores larger ciphertext only
```

## Required Invariants

- Share link path and query contain only secret IDs, never encryption keys.
  Model B secrets additionally carry a raw decryption key in the URL fragment
  (`#key=...`); fragments are never transmitted to the API, so the key stays
  client-side. Keys must never appear in the path or query string, where the
  server and access logs would see them.
- Passphrases and derived keys must never be sent to the API.
- Ciphertext payloads are returned only by a verified open operation that marks
  the secret consumed in the same transaction.
- Five invalid access-proof attempts mark the secret consumed and remove the
  stored ciphertext payload.
- NATS messages must never contain plaintext, passphrases, derived keys, or
  ciphertext bodies.
- Logs, metrics, and trace spans must not include plaintext, passphrases,
  derived keys, or full ciphertext bodies. Span attributes carry only safe
  metadata (route, content kind, storage backend, status), never secret content.
- Filenames stored server-side must be encrypted or opaque.
- File names are encrypted in the browser as metadata before upload.
- Internal worker to API calls require `FLICK_INTERNAL_TOKEN`.
- The `/metrics` endpoint requires a separate bearer token (`FLICK_METRICS_TOKEN`); it never serves secret content, and an unset token fails closed (401).
- Public repository files must not contain real credentials or production
  configuration.
- A secret is either Model A or Model B, never a mix. Model A secrets carry an
  access proof hash and access KDF parameters; Model B secrets carry neither.
  A request that provides a proof without KDF (or vice versa) must be rejected.

## Encryption

Initial encryption and KDF target:

- Web Crypto AES-GCM.
- PBKDF2-HMAC-SHA256 through Web Crypto for MVP.
- 600,000 PBKDF2 iterations or more.
- Random 128-bit or larger salt per secret.
- Random nonce per encrypted payload.
- Derived key used only in browser memory.

The API stores nonce, KDF salt, and KDF parameters because they are required for
browser-side decrypt. These values are not secret.

Access proof KDF parameters are stored separately from encryption KDF
parameters. They allow the browser to reproduce the proof before the API returns
the ciphertext payload. The API compares proof hashes and never receives the
encryption key.

Argon2id is the preferred memory-hard direction for a later release, but it
requires a browser WASM dependency and supply-chain review.

## Access Control Models

Flick supports two access control models. Both keep the plaintext out of the
server: the difference is how the open operation is authorized.

### Model A — Passphrase Required (Default)

The browser derives both the encryption key and a separate access proof from a
user-entered passphrase, using distinct KDF parameters. The API stores only a
hash of the access proof and requires a matching proof on every open attempt.
Because the access-proof KDF salt differs from the encryption KDF salt, a
captured access proof cannot reproduce the encryption key.

Security properties:

- The server cannot return the ciphertext without a valid access proof.
- Five invalid proof attempts mark the secret consumed and delete the payload.
- Suitable when the receiver is expected to know a shared passphrase.

### Model B — Passphrase Optional (Link-Bearer)

The browser generates a random 256-bit key, encrypts the payload directly with
it (no KDF), and places the raw key in the URL fragment (`#key=...`). The API
stores `NULL` for the access KDF and access proof hash, and the open endpoint
performs no proof validation. Whoever holds the full link (id plus fragment
key) is authorized; the link is the capability.

Security properties:

- The server still never sees the key or plaintext: the key lives only in the
  fragment, which browsers do not transmit, and decryption happens in the
  browser.
- One-time open (`max_views = 1`) bounds replay: a captured key can authorize
  at most a single open, after which the payload is deleted.
- Requires an honest-server assumption: a server that serves malicious client
  code could exfiltrate the fragment key from the browser. This is the
  web-E2EE limit shared with Model A; see Residual Risk.

The key must travel only in the fragment. Placing it in the path or query
string would send it to the API and into access logs, at which point the server
could decrypt the payload on open — breaking the core invariant.

## Structured Credentials

Structured credentials are a browser-side text-secret encoding, not a new server
secret kind.

The create page serializes credential templates as `FLCR1:` followed by JSON
matching `contracts/credential-payload.schema.json`, then encrypts that string
through the existing text-secret path. The API stores and returns the encrypted
payload as `kind:"text"` and never sees field labels, values, notes, titles, or
which fields were marked secret.

The `secret` field in the credential payload is a UI rendering hint. It tells
the browser to mask a value before encryption and after decryption, but it is
not a security boundary. All credential fields, notes, and titles rely on the
same browser-side encryption, access proof gate, one-time open, and TTL cleanup
as ordinary text secrets.

## Deletion and Residual Risk

Deleting a secret means Flick no longer serves its ciphertext and no server
component knows the passphrase or derived key.

Residual risks remain:

- SQLite may retain deleted ciphertext in WAL or freelist pages until checkpoint
  or vacuum.
- Backups may retain old ciphertext.
- Clipboard managers, screenshots, or chat history may retain the passphrase if
  users share it insecurely.
- A compromised browser can read plaintext before encryption or after decrypt.
- A compromised server can deny service, delete data early, or serve malicious
  web assets.
- Flick's web client trusts the server to deliver untampered JavaScript. A
  server that actively turns malicious (as opposed to one whose data merely
  leaks) can serve modified client code that exfiltrates the passphrase or
  Model B fragment key during open, recovering the plaintext. The design
  therefore targets passive leakage (database, log, backup exposure), not an
  actively malicious server. A native/CLI client that the server cannot
  re-deliver would close this gap but is deferred for now due to sharing
  friction.

Runbooks must document checkpoint/vacuum and backup retention behavior. The
product should communicate that Flick is ephemeral delivery, not guaranteed
physical erasure.

## Transport

Production deployments require HTTPS at the ingress. Internal cluster traffic
may start as plain HTTP protected by namespace boundaries and internal tokens,
but this is not a substitute for public TLS.

## Web Response Headers

The web app is a static SPA served by nginx (`web/nginx.conf`). Because the
plaintext, passphrase, and derived key live only in the browser, the headers
that constrain page-context code are part of the trust boundary, not cosmetic
hardening. They apply to the HTML responses (`location /`); a CSP on the static
sub-resources would be meaningless, so only `nosniff` is re-asserted there.

| Header | Value | Purpose |
| --- | --- | --- |
| `X-Frame-Options` | `DENY` (with CSP `frame-ancestors 'none'`) | Create/open pages are not framable — anti-clickjacking. |
| `X-Content-Type-Options` | `nosniff` | No MIME sniffing (also on the immutable assets). |
| `Referrer-Policy` | `no-referrer` | Never leak the `/s/{id}` path on outbound navigation. |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` | The app uses none of these. |

CSP directives:

| Directive | Value | Why |
| --- | --- | --- |
| `default-src` | `'self'` | Same-origin baseline for anything not named below. |
| `script-src` | `'self' 'unsafe-inline'` | The SvelteKit hydration bootstrap is inline and its hash changes every build, so nginx cannot pin it; app modules load from `'self'`. See the limitation below. |
| `style-src` | `'self' 'unsafe-inline'` | Tailwind v4 injects runtime `<style>`; `app.html` uses an inline `style` attribute. |
| `img-src` | `'self' data:` | A CSS background uses an inline `data:` SVG. |
| `connect-src` | `'self' ${FLICK_CSP_CONNECT_SRC}` | API is same-origin; the object-store origin for direct large-file uploads is appended at runtime (empty when the S3 backend is disabled). |
| `frame-ancestors` | `'none'` | Anti-clickjacking. |
| `base-uri`, `form-action` | `'self'` | No base-tag hijack, no cross-origin form posts. |
| `object-src` | `'none'` | No plugins. |

`connect-src` is the load-bearing directive: even an injected script (which
`'unsafe-inline'` does not stop) cannot exfiltrate the plaintext or passphrase to
an arbitrary origin — it may only reach `'self'` and the configured object store.
The S3 origin is deployment-specific and unknown at web build time (one shared
image), so the CSP is rendered by the nginx envsubst entrypoint from
`FLICK_CSP_CONNECT_SRC` (see [env contract](env-contract.md)); enabling
`FLICK_STORAGE_LARGE_BACKEND=s3` means setting it to the `FLICK_S3_ENDPOINT`
origin.

Limitation and upgrade path: `script-src 'unsafe-inline'` is weaker than a
hash/nonce policy. Hashing is build-time only, but `connect-src` must stay
runtime, and a `<meta>` CSP and an nginx CSP *intersect* rather than union — so
the two cannot be combined to both pin scripts and allow a runtime origin. The
app renders no untrusted HTML and `connect-src` already bounds exfiltration, so
the inline-script allowance is an accepted trade-off until the bootstrap is
externalized.

`Strict-Transport-Security` is intentionally not set on the pod (plain `:8080`,
local `flick.localhost` base ingress); production overlays add HSTS at the
TLS-terminating ingress (see Transport above).

## Future Security Features

- Argon2id client-side KDF after WASM dependency review
- hash/nonce `script-src` to drop the inline-script allowance (see Web Response
  Headers for why it is deferred)
- admin audit viewer without sensitive values
- optional notification without revealing secret contents
- native/CLI client to defend against an actively malicious server (deferred —
  sharing friction currently outweighs the benefit; the honest-server
  assumption is documented above)
