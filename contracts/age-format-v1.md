# age Format v1

Status: current payload format for every Flick client. `internal/clientcrypto/age.go`
(used by the `flick` CLI in `internal/flickcli`) and `web/src/lib/crypto/age.ts`
(used by the browser) implement the client side, and the API stores and streams
age files without reading them (see API Mapping). Send links, request links,
and the live relay all use this format.

## Why age

The AES-GCM format seals a whole file in one AEAD call, so both clients hold the
full plaintext and ciphertext in memory. age ([c2sp.org/age](https://c2sp.org/age))
splits the payload into 64 KiB ChaCha20-Poly1305 chunks and authenticates each
chunk before releasing it, so a file can be encrypted while it is read and
decrypted while it downloads. A truncated or reordered file fails instead of
producing a short plaintext.

Flick uses only standard age recipients, identities, and files. It defines no
cipher, KDF, nonce scheme, or chunk layout of its own. Implementations:

| Client | Library | Version | License |
| --- | --- | --- | --- |
| Go (CLI) | `filippo.io/age` | v1.3.2 | BSD-3-Clause |
| Browser | `age-encryption` | 0.3.1 | BSD-3-Clause |

Both libraries are maintained by the age author and test against the shared
CCTV vectors ([c2sp.org/CCTV/age](https://c2sp.org/CCTV/age)).

## Per-Secret Identity

The sending client generates one fresh hybrid identity per secret:

```text
AGE-SECRET-KEY-PQ-1...   77 characters, ML-KEM-768 + X25519
```

The hybrid type keeps stored ciphertext safe against a future quantum attacker
who recorded it. Its recipient (`age1pq1...`, about 1.9 KB) is used only inside
the sending client and is never stored. Each encrypted file header is about
1.6 KB.

Clients accept only this identity type (`IdentityPrefix` /
`IDENTITY_PREFIX`). Any other identity is rejected before decryption.

## Encrypted Objects

Each object below is one binary (not armored) age file encrypted to the secret's
recipient. age gives each file its own random file key, so the files are
independent.

| Object | Plaintext | Carried as |
| --- | --- | --- |
| body | text secret (UTF-8) or file bytes | inline column or object storage, raw bytes |
| filename | UTF-8 filename (file secrets only) | API field, base64 |

The plaintext size limit is the deployment's `FLICK_MAX_FILE_BYTES`
(default 268,435,456 bytes = 256 MiB, `internal/config/defaults.go`), which
`GET /api/config` advertises as `max_file_bytes`. The browser
(`web/src/lib/api/config.ts`) and the CLI (`internal/flickcli/client.go`) read
that value and fall back to the same 256 MiB default only when `/api/config`
is unreachable. A deployment without object storage advertises a smaller
effective limit (`internal/config/config.go:EffectiveMaxFileBytes`).

Ciphertext size is exact and depends only on the header length `H` and the
plaintext length `n`:

```text
size = H + 16 + n + 16 * max(1, ceil(n / 65536))
```

The 16 bytes after the header are the payload nonce, and each chunk adds a
16-byte tag. An empty plaintext still has one chunk. The browser library exposes
the same arithmetic as `ReadableStreamWithSize.size()`.

## Model B: Identity in the Fragment

```text
https://<host>/s/<id>#key=AGE-SECRET-KEY-PQ-1...
```

The fragment carries the identity string unchanged. It is never sent to the API,
as in `docs/architecture/security-model.md`. The base64url raw key fragment of
the AES-GCM format is not accepted.

## Model A: Identity Wrapped by the Passphrase

The client encrypts the identity string with an age passphrase recipient
(`scrypt` stanza) and sends the resulting age file to the API as
`wrapped_identity`. The API stores it and returns it only from the verified
open, together with the body.

- Work factor: `18` (log2 N), age's default, about 256 MiB of scrypt memory.
- Clients refuse a wrapped identity whose stanza asks for more than `20`. The
  browser library has this cap built in, and the Go client sets it explicitly
  (`MaxWrapWorkFactor`), so a file one client opens, the other opens too. The
  file arrives from the server; the cap stops it from exhausting the client.
- A wrong passphrase reads as `ErrDecrypt` in Go and as a rejected promise in the
  browser, the same as a wrong key.

Wrapping the identity, instead of encrypting the body directly with the
passphrase, costs one scrypt run per open for body and filename together, and
keeps the body format identical for Model A and Model B.

The access proof is unchanged: PBKDF2-SHA-256, 600,000 iterations, its own salt
(`internal/clientcrypto/access.go:DeriveAccessProof`). Because the server holds
the proof hash, a server-side attacker can test passphrases at PBKDF2 cost
regardless of the scrypt work factor. scrypt raises the cost only for someone
who obtains the stored files without the database. Passphrase strength remains
the real protection.

## Untrusted Input

Everything a client decrypts arrives from the server or a URL.

- Parse identities with `ParseIdentity` / `parseSecretIdentity`.
- Treat an unwrapped identity larger than 1 KiB as invalid.
- Never show plaintext from a stream that has not reached its end without an
  error. The final chunk proves the file is complete.

## Interop Vectors

`tests/fixtures/age/vectors.json` holds one block produced by each client:
identity, wrapped identity, text, filename, and a three-chunk file
(`go-file.age`, `web-file.age`, 150,001 bytes of `i % 251`). Both
`internal/clientcrypto/age_test.go` and `web/src/lib/crypto/age.test.ts` open
both blocks and check the size formula.

age output is randomized and cannot be regenerated byte-for-byte. Add a block
for a new case; do not replace an existing one. A block that stops opening means
a client changed the format.

## API Mapping

`contracts/openapi.yaml` carries these objects as follows.

| Object | Create (`POST /api/secrets`) | Open (`POST /api/secrets/{id}/open`) |
| --- | --- | --- |
| body | `ciphertext` (base64, inline) or presigned PUT (raw) | response body, raw bytes |
| body length | `size_bytes`, exact age file length | `Content-Length` |
| filename | `encrypted_filename` (base64) | `Flick-Encrypted-Filename` header |
| Model A wrapped identity | `wrapped_identity` (base64) | `Flick-Wrapped-Identity` header |

- The server checks lengths and the age magic line only. It cannot read
  anything inside.
- There is no presigned GET. The API streams the body from the bucket. It
  starts the bucket read before committing the open (which deletes the secret
  row), so a bucket that cannot serve the body leaves the secret openable. A transfer that breaks after
  the response has started loses the secret; the recipient's age decryption
  then fails at the missing final chunk instead of showing a partial file.
- A stream may last at most 30 minutes (`secrets.OpenStreamTimeout`). The API
  queues the bucket delete when the stream ends. The open transaction records
  the object key in `secret_open_grace` for 35 minutes, and object
  reconciliation leaves that key alone until then, so it never deletes a body
  that is still streaming. An opened request link keeps its row and
  `consumed_at` for the same 35 minutes
  (`docs/architecture/database-schema.md`).
- Limits: `/api/config` advertises sizes before encryption. The server's
  ciphertext caps add `AgeHeaderAllowanceBytes` (4,096), the 16-byte payload
  nonce, and 16 bytes per 64 KiB chunk (`internal/secrets/age.go`).

## Request Links

Request links use the same age files, encrypted to a per-request hybrid
recipient that the requester's browser generates
(`docs/architecture/request-links.md`). The recipient is `public_key`; the
identity travels only in the private retrieval fragment (`#receive=…&key=…`).
There is no passphrase and no wrapped identity. Bodies and filenames map to the
API as for secrets: `size_bytes` is the exact age file length, the filename is
`envelope.encrypted_filename` on submit, and open returns the raw body with
`Flick-Kind` and `Flick-Encrypted-Filename` headers.

## Live Relay (Send with Code)

The live relay (`/api/pipe/*`, `internal/piperelay`) streams one age file from
a sending browser to a receiving browser through API memory. Nothing is stored
in SQLite or object storage, and the 6-digit code only routes requests; it is
not key material. The full threat model is in
`docs/architecture/security-model.md`, section "Live Relay (Send with Code)".

- **Receiver identity.** For each receive attempt the receiving browser
  generates a fresh hybrid identity (`AGE-SECRET-KEY-PQ-1...`, the same type as
  Per-Secret Identity above) with `createPipeReceiverKey` in
  `web/src/lib/crypto/pipe.ts`. The identity stays in that browser's memory
  only: it is never stored, put in a URL, or sent.
- **Recipient on join.** `POST /api/pipe/join` sends only the recipient
  (`{"recipient":"age1pq1..."}`, `joinPipe` in `web/src/lib/crypto/pipe.ts`).
  `internal/httpapi/piperelay.go:validPipeRecipient` accepts only a hybrid
  recipient and answers `400 invalid_recipient` otherwise. The API hands
  recipients only to the sender token holder, through `GET /api/pipe/wait`.
- **Sender encryption.** The sender encrypts the body stream to that recipient
  (`encryptToRecipient` from `web/src/lib/crypto/age.ts`, called in
  `web/src/lib/state/pipe-relay.svelte.ts:streamPayload`) and, for a file, the
  UTF-8 filename as a separate age file (`encryptPipeFilename`).
- **Wire mapping.** `POST /api/pipe/send` carries the body as raw age bytes
  (`Content-Type: application/octet-stream`) with `Flick-Kind` and, for files,
  `Flick-Encrypted-Filename` (base64 age file). `GET /api/pipe/receive` returns
  the same raw stream and headers. The receiver decrypts the body with
  `decryptWith` and the filename with `decryptPipeFilename`.

There is no passphrase and no wrapped identity in a code transfer. Known limit:
the recipient exchange is not authenticated against the server, so an actively
malicious server can substitute its own recipient on `/wait` (a
man-in-the-middle); the 3-digit confirmation number does not detect this
because the server draws it and it is not derived from either key.
