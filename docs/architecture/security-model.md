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

For Model A, the browser also derives a separate access proof from the same
user input with separate KDF parameters. The API stores only a hash of that proof. The proof
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
- Ciphertext payloads are returned only by a verified open operation that
  deletes the secret and its management record in the same transaction.
- The Model A wrapped identity is returned only by that verified open, never by
  metadata reads: anyone holding it can test passphrases offline at scrypt cost
  with no lockout.
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
- The `/metrics` endpoint requires a separate bearer token (`FLICK_METRICS_TOKEN`); it never serves secret content, and an unset token fails closed (401). The worker exposes the same credential on its dedicated metrics listener (`FLICK_WORKER_METRICS_ADDR`, default `:9091`), which is ClusterIP-only.
- Usage and access observability is admin-only and lives off the public site:
  daily aggregate counters (`internal/usage`) are written with each state
  change and have no HTTP reader, admin dashboards run on the cluster-side
  Grafana (separate subdomain with its own login), and access-log search sees
  IPs/paths/status codes but never secret content, passphrases, or ciphertext.
- Public repository files must not contain real credentials or production
  configuration.
- A secret is either Model A or Model B, never a mix. Model A secrets carry an
  access proof hash and access KDF parameters; Model B secrets carry neither.
  A request that provides a proof without KDF (or vice versa) must be rejected.

## Encryption

Secrets use standard age files ([age format v1](../../contracts/age-format-v1.md)):

- A fresh hybrid age identity per secret, generated in the client
  (`age-encryption` in the browser, `filippo.io/age` in the CLI).
- 64 KiB authenticated chunks, so files encrypt and decrypt as streams. A client
  shows content only after the final chunk authenticates.
- Model A wraps the identity in an age scrypt passphrase file (work factor 18;
  clients refuse more than 20). Model B carries the identity in the link
  fragment.
- Derived keys and identities stay in client memory.

The API stores the age body, the age-encrypted filename, and for Model A the
wrapped identity. None of these is secret without the passphrase or the link.

The Model A access proof stays PBKDF2-HMAC-SHA256 (600,000 to 6,000,000
iterations, random 128-bit salt), stored separately from the wrapped identity. It lets the
browser prove the passphrase before the API returns the body. The API compares
proof hashes and never receives the passphrase or the identity in the clear.

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
- The create page can generate the passphrase in the browser
  (`web/src/lib/crypto/passphrase.ts`): 16 Crockford base32 symbols, 80 bits
  from `crypto.getRandomValues`, held only in page memory like a typed
  passphrase. A user-chosen passphrase can be much weaker; its strength, with
  the PBKDF2 cost, is what limits an offline guess against the stored
  access-proof hash.

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
- One-time open (`max_views = 1`) bounds ciphertext release. The API accepts
  the ID without checking the fragment key; the browser needs the key to
  decrypt. Possession of an ID alone can therefore consume a Model B delivery.
- Requires an honest-server assumption: a server that serves malicious client
  code could exfiltrate the fragment key from the browser. This is the
  web-E2EE limit shared with Model A; see Residual Risk.

The key must travel only in the fragment. Placing it in the path or query
string would send it to the API and into access logs, at which point the server
could decrypt the payload on open — breaking the core invariant.

## One-time requests

[Request links](request-links.md) define independent submission/retrieval
tokens and standard age files encrypted to a per-request hybrid ML-KEM-768 +
X25519 recipient. The public submission link cannot retrieve or decrypt
content. The private retrieval link deliberately carries the requester's age
identity in its fragment, with explicit custody and history-exposure guidance.
The API keeps separate SHA-256 capability hashes, accepts only a canonical
hybrid recipient and the version 2 envelope, checks the age header magic, and
releases the ciphertext once through non-consuming authenticated metadata and
an atomic consuming open. It cannot authenticate anything inside an age file;
the requester's decryption does. The request ID and kind are not bound into the
ciphertext: each request has its own identity, so a body cannot move to another
request, but the server could relabel its kind, which changes only how the
browser presents the bytes. HTTP spans contain only method, route template, and
status; request bodies and Authorization are excluded. The browser checks that
its identity's recipient matches owner metadata before Open, and keeps decrypted
content and retry receipts only in memory. Large file reservations bind one
generation, attempt token, immutable envelope, size, and ciphertext SHA-256.
Finalize streams the upload into a pre-reserved server-only final key and
accepts it only if the bytes it wrote match that length and checksum; a signed
staging PUT cannot overwrite accepted content. The API rechecks reservation and
original expiry at acceptance, and object read failures never consume.
Generation exhaustion returns unavailable. Recurring request-prefix cleanup
handles late writes even after capability metadata is gone.

## Live Relay (Send with Code)

The live relay (`/api/pipe/*`, `internal/piperelay`) streams an age file from
one browser to another through API memory; it writes nothing to SQLite or
object storage. The browser derives the age passphrase from the 6-digit code,
the server-issued salt, and an optional extra passphrase
(`web/src/lib/crypto/pipe.ts` `computePipePassphrase`). The server issues both
the code and the salt, so without the extra passphrase this mode trusts the
server like Model B trusts the link holder: a server that turns malicious could
decrypt the stream. It is not end-to-end encrypted against the server.

Credentials and what they unlock:

- `POST /api/pipe/init` returns the 6-digit code, the salt, and a random sender
  token. Only the sender token unlocks the sender calls (`/wait`, `/select`,
  `/decline`, `/cancel`, `/send`). A wrong token answers `404` exactly like an
  unknown code, so those calls are no code oracle, and knowing the 6-digit code
  is not enough to watch the list, pick a device, cancel, or inject a stream.
  With the right token, `/select` and `/decline` answer `409 not_pending` for a
  connection that already left or never existed.
- `POST /api/pipe/join` is the only call that accepts a bare 6-digit code. It is
  rate-limited by `FLICK_OPEN_RATE_PER_MIN` per client network: the client IP
  resolved through `FLICK_TRUSTED_PROXIES`, with an IPv6 address collapsed to
  its /64 (`internal/httpapi/ratelimit.go` `clientNetwork`), because one IPv6
  subscriber usually holds a whole /64 and could otherwise rotate through
  2^64 fresh buckets. `/api/pipe/join` and `/api/v1/pipe/join` spend one
  bucket. It returns an unguessable connection ID that `/status` and
  `/receive` need. Every other rate-limited endpoint keys by the same client
  network (`internal/httpapi/ratelimit.go` `rateLimiter.middleware`).

**Confirmation on ("Require confirmation before transfer").** The session is a
waiting room. Every device that enters the right code is held as a pending
connection, up to `MaxPendingConnections` (10) in
`internal/piperelay/coordinator.go`. Each connection gets its own 3-digit
verification number from crypto/rand, unique among the waiting connections and
independent of the 6-digit code, so knowing the code reveals nothing about any
device's number. The receiver reads its number to the sender over another
channel (voice, chat); the sender sees every waiting connection with its
number, a coarse device label such as "Safari on iPhone", and its wait time,
then sends to the matching row. Every other waiting device ends with "The sender
chose another device" and receives nothing. The check is the sender's eyes:
there is no receiver-side typing or mutual confirmation, and it proves only that
the stream goes to the device that showed that number. It does not defend
against a compromised server, which draws the numbers.

A waiting receiver long-polls `/status` (up to 25 seconds per poll) and opens
`/receive` only after `/status` reports it selected; `/receive` on a connection
that is still waiting answers `409 awaiting_decision`. No waiting request is
ever silent for long enough to be cut by a proxy idle limit (Cloudflare cuts at
100 seconds). A waiting connection counts as present only while it polls: one
with no open `/status` call for 35 seconds
(`internal/piperelay/coordinator.go` `defaultPresenceTimeout`) drops out of the
list, which frees its slot without a decline, a block, or a burn. A selected
connection that does not open `/receive` within the same 35 seconds ends the
session, like the chosen receiver leaving before the stream starts. One client
network (an IPv4 address or an IPv6 /64) can hold at most 3 waiting
connections per session (`MaxPendingPerNetwork`); the next one gets `409
network_full`.

One connection holds at most 2 open `/status` calls
(`MaxStatusPollsPerConnection`; the next gets `409 poll_in_progress`), and
`/status` is rate-limited to 120 calls a minute per client network
(`internal/httpapi/piperelay.go` `pipeStatusRatePerMinute`), so a connection ID
cannot pile up server goroutines and sockets.

Residual risk: someone who holds the right 6-digit code can keep slots for the
session's 3-minute window in two ways. They can keep polling `/status`, or they
can skip polling and re-join roughly every 35 seconds as each idle connection
drops, spending only `/join` rate-limit tokens. The per-network cap means
filling all 10 slots takes at least 4 networks, and the sender sees every such
row and can deny it, but a code holder with many networks can still crowd the
room until the sender asks for a new code. Keying by IPv6 /64 also still hands
a holder of a larger prefix (a /56 has 256 /64s, a /48 has 65,536) that many
separate join buckets and per-network caps. A global or per-session cap on
joins is a possible future hardening; it is not implemented.

The sender can deny a single row. That device ends with "The sender declined
this device", its slot reopens, and the session stays alive. The API also blocks
the same client from rejoining the session: it keeps a SHA-256 of the client
network (the client IP resolved through `FLICK_TRUSTED_PROXIES`, the same way as
the rate limiter, with IPv6 collapsed to its /64) and the User-Agent, in memory
only, for the session's lifetime (`internal/httpapi/piperelay.go`
`pipeJoinClient`). The block is deliberately coarse. Another device behind the
same NAT or IPv6 /64 with the same browser and OS string is blocked too for that
one short session, and a determined person can change network or User-Agent to
get back in; the sender can deny them again. The declined browser also
remembers the decline in `sessionStorage` as a SHA-256 of the code plus the
session's expiry time, computed once when `/join` answered
(`web/src/lib/state/pipe-declined.ts` `sessionExpiresAt`), so a reload does not
ask again and the memory lapses with that session. The entry never holds the
code itself, the salt, or the passphrase, and a 6-digit code's hash is
brute-forceable, so it is treated as no more secret than the code the user
already typed on that device.

**Confirmation off.** The first device to open `/receive` wins and every later
device gets `409 already_connected`. Anyone who learns the 6-digit code first
can take the transfer, so this mode suits codes shared over a channel the
sender trusts.

**No burn on rejected receivers.** A wrong 6-digit code never reaches a session
(the code is the lookup key), so a per-session failed-attempt counter can only
ever count people who held the right code. Late, extra, declined, idle,
room-full, and network-full receivers therefore never end a session or count
against it; online code guessing is bounded only by the `/join` rate limit (per
client network, as above) and the 3-minute window. An 11th waiting device gets
`409 room_full` and the session stays alive. `/join` and `/select` answer `410
expired` once the window closed, even before cleanup removes the session.

**A started stream is never cut by a burn.** Sender cancel returns `409
streaming` once bytes flow, and the rendezvous TTL cleanup skips streaming
sessions until `maxTransferDuration` (10 minutes). Cancel and the cleanup check
for streaming and end the session under the same lock that `/send` holds to
mark the stream started, so a cancel racing a starting stream either ends the
session before any byte flows or loses with `409 streaming`. Only the stream
itself failing, either side disconnecting, or a server shutdown ends it.

The API never logs 6-digit codes, verification numbers, sender tokens,
connection IDs, client keys, or User-Agents. Device labels live in memory only,
for the session's lifetime.

## Structured Credentials

Structured credentials are a browser-side text-secret encoding, not a new server
secret kind.

The create page serializes credential templates as `FLCR1:` followed by JSON
matching `contracts/credential-payload.schema.json`, then encrypts that string
through the existing text-secret path. The request submission page uses the same
encoding and submits it as a `kind:"text"` request payload. The API stores and
returns the encrypted payload as `kind:"text"` and never sees field labels,
values, notes, titles, or which fields were marked secret.

The `secret` field in the credential payload is a UI rendering hint. It tells
the browser to mask a value before encryption and after decryption, but it is
not a security boundary. All credential fields, notes, and titles get the same
browser-side encryption, open requirements, one-time open, and TTL cleanup as an
ordinary text payload on the same path, whether a secret link or a request.

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
  actively malicious server, for anyone using the browser.

  The `flick` CLI (`cmd/flick`) does close this gap for its own users. It is
  installed once and the server cannot re-deliver it, so a server that turns
  malicious cannot swap out the code that holds the passphrase or the fragment
  key. That protection is only as good as the install: it depends on the
  binary being fetched from a release built by `.github/workflows/release-cli.yml`
  from a commit already on `main`, and on the downloader checking `SHA256SUMS`.
  The CLI carries its own copy of the client crypto in `internal/clientcrypto`,
  which is a second implementation of the same wire format and is pinned to the
  browser's by the shared golden vectors in
  `tests/fixtures/client-crypto-vectors.json` — see that package's `AGENTS.md`
  for why those vectors must not be regenerated to make a test pass.

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

## Advertising boundary

The app origin must not load third-party advertising or analytics scripts,
including on public routes that share the SvelteKit runtime. Creation, open,
management, and request pages handle browser-only secrets. M10 permits only
an optional first-party static sponsor on public guides; the scope, separate
origin requirement for future ad-network trials, and aggregate measurement
rules are defined in [Advertising experiment](advertising-experiment.md).

## Future Security Features

- Argon2id client-side KDF after WASM dependency review
- hash/nonce `script-src` to drop the inline-script allowance (see Web Response
  Headers for why it is deferred)
- admin audit viewer without sensitive values
- optional notification without revealing secret contents
- reproducible or signed CLI release artifacts, so a downloader can verify more
  than the `SHA256SUMS` published alongside the binaries

## M8 Sender Management

[Sender management v1](../../contracts/sender-management-v1.md) is the accepted
design. #200 implements issuance and status; #201 implements cancellation and
recurring object cleanup. `web/src/lib/components/ManageSecretPage.svelte` implements
the #202 browser status/cancellation flow. New deliveries get an independent
32-byte random management bearer; the API stores only its SHA-256 hash. The
private management fragment never contains the recipient encryption key. Status
and cancellation authority end at the original content expiry; neither operation
returns content or grants decryption. Initial recipient-link sharing uses a
browser-memory handoff. Refresh or a different device offers status/cancellation
only, with no recipient-link recovery.

A committed one-time open is a release decision, not evidence of a human read.
Opened means forgotten: the open transaction deletes the secret and its
management record, so the server cannot later say that anything existed under
that ID. Management status, cancellation (including a cancellation that loses
the race to an open), recipient metadata, and a repeated open all return the
response an unknown ID gets, byte for byte. Management still distinguishes
invalid-proof lockout and sender cancellation, which keep their outcomes until
the original expiry. Residual traces remain outside that guarantee:

- a bucket body's `managed/secrets/{id}` key stays in `secret_open_grace` for
  35 minutes;
- the same key stays in its `delete_oci_object` outbox row until the reaper
  prunes it one hour after publish, and indefinitely in a `failed` outbox row
  kept for diagnosis;
- while a delete job is live, NATS JetStream holds the key in the message until
  the worker acknowledges it;
- worker failures carry no key: `internal/worker/cleanup.go:objectDeleteError`
  names the job ID, reason, and backend and masks the key in the bucket SDK's
  message for `job_receipts.last_error`, `job_attempts.error`, `dead_letters`,
  and the `worker job failed` log line, and
  `internal/worker/processor.go:deadLetterPayloadJSON` drops `object_key` from
  a dead-lettered object delete;
- a job ID still links worker rows to an outbox row while that row exists;
- proxy access logs record request paths, which include the secret ID;
- SQLite WAL pages and backups may hold deleted rows.

Raw management tokens belong only in the private browser fragment,
the creation response, and the management Authorization header; exclude them
from persistent browser storage, logs, telemetry, and worker messages.
