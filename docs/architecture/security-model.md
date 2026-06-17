# Security Model

BurnLink is designed around client-side encryption and short retention.

## Security Goal

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

The browser derives an encryption key from a user-entered passphrase and
encrypts payloads before upload. The API, worker, NATS, SQLite, OCI Object
Storage, logs, and metrics handle ciphertext and safe metadata only.

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

burnlink-web
  serves UI and public client config only

burnlink-api
  stores ciphertext, metadata, and access proof hashes
  owns api.db
  publishes outbox-backed jobs

burnlink-worker
  consumes jobs
  owns worker.db
  calls internal API endpoints

NATS JetStream
  stores job IDs and small metadata only

OCI Object Storage
  stores larger ciphertext only
```

## Required Invariants

- Share links contain only secret IDs, not encryption keys.
- Passphrases and derived keys must never be sent to the API.
- Ciphertext payloads are returned only by a verified open operation that marks
  the secret consumed in the same transaction.
- Five invalid access-proof attempts mark the secret consumed and remove the
  stored ciphertext payload.
- NATS messages must never contain plaintext, passphrases, derived keys, or
  ciphertext bodies.
- Logs and metrics must not include plaintext, passphrases, derived keys, or
  full ciphertext bodies.
- Filenames stored server-side must be encrypted or opaque.
- File names are encrypted in the browser as metadata before upload.
- Internal worker to API calls require `BURNLINK_INTERNAL_TOKEN`.
- Public repository files must not contain real credentials or production
  configuration.

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

## Deletion and Residual Risk

Deleting a secret means BurnLink no longer serves its ciphertext and no server
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

Runbooks must document checkpoint/vacuum and backup retention behavior. The
product should communicate that BurnLink is ephemeral delivery, not guaranteed
physical erasure.

## Transport

Production deployments require HTTPS at the ingress. Internal cluster traffic
may start as plain HTTP protected by namespace boundaries and internal tokens,
but this is not a substitute for public TLS.

## Future Security Features

- Argon2id client-side KDF after WASM dependency review
- rate limiting
- stricter Content Security Policy
- admin audit viewer without sensitive values
- optional notification without revealing secret contents
