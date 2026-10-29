# One-time request links

Status: request links use the age format (M13). #288 moved the API, schema and
contract; #289 moves the browser. The first version (#203–#207) used RSA-OAEP
and AES-GCM; that format is gone, and the API migration drops its rows (see
"Ownership and implementation boundaries"). Send links follow
`contracts/age-format-v1.md`; sender management follows the separate #199
contract.

## Product and authority

A requester creates one short-lived request and saves a private retrieval link.
The requester shares a separate submission link. The first accepted submission
wins; the requester can release its ciphertext once and decrypt locally. There
are no accounts, repeat collection, permanent inboxes, or recovery service.

The requester browser generates a fresh age identity before creation. The API
receives only its recipient (the public half) and issues independent 32-byte
random submission and retrieval tokens. Both use unpadded base64url; the API
stores SHA-256 hashes, never raw tokens. Tokens are unrelated to key material
and the request ID.

| Capability | Read submission instructions | Submit | Read owner status/revoke | Release ciphertext | Decrypt |
| --- | --- | --- | --- | --- | --- |
| ID alone | No | No | No | No | No |
| Submission token and recipient fingerprint | Yes | Once | No | No | No |
| Retrieval token | No | No | Yes | Once | No |
| age identity alone | No | No | No | No | Only with ciphertext |
| Private retrieval link (token and identity) | No | No | Yes | Once | Yes |

Submission URL: `/r/{id}#submit=<token>&fp=<fingerprint>`.
Private retrieval URL: `/r/{id}/receive#receive=<token>&key=<identity>`.
Fragments never enter HTTP requests. The appropriate token travels only in
`Authorization: Bearer <token>`, never in a query or path. Cross-role tokens
fail authorization. Apply `Cache-Control: no-store` to all request API responses,
allow Authorization only for the configured web CORS origin, and exclude
headers, bodies, URLs, tokens, and key material from logs and telemetry.

The submission fragment's fingerprint pins the recipient against accidental
or server-side substitution when the client code is honest. Whoever can replace
the shared link can replace its fingerprint. A malicious server can still serve
malicious JavaScript; this browser limitation is the same as ordinary send links.
No requester identity or submitter identity is authenticated.

## Key custody

Only the requester browser generates the identity. It is a hybrid ML-KEM-768 +
X25519 age identity (`AGE-SECRET-KEY-PQ-1…`, 77 characters), the same type a
Model B send link carries. It goes into the private retrieval fragment as is and
is never transmitted to the API. Keep identities and decrypted data in memory.
Do not put them in localStorage, sessionStorage, IndexedDB, analytics, or Svelte
history state.

The private retrieval URL deliberately contains the identity, unlike an ordinary
sender management URL. Saving or reopening that complete private URL supports
refresh, browser restart, and another device while the request is valid. The
UI must explain that clipboard managers, browser history/sync, screenshots, and
anyone receiving the link may retain the complete retrieval capability. Do not
promise the URL is absent from history. A fragment-free URL cannot recover it.
Losing the complete link or identity means the submission cannot be recovered.

On the retrieval page, parse the identity and derive its recipient before
enabling Open. The derived recipient must equal the `public_key` from owner
status; a mismatch means the link pairs the wrong identity with this request.
Status reads do not consume. After consumption, a refresh cannot retrieve
again; retain the plaintext/download only in memory. Do not automatically open,
replay an open, or retry a lost open response.

## Encryption format

A submission is standard age, as `contracts/age-format-v1.md` pins it for send
links. There is no Flick-specific construction.

- `public_key` is the hybrid recipient (`age1pq1…`, about 1,960 characters) in
  its canonical lowercase Bech32 string. The API parses it with
  `age.ParseHybridRecipient` and accepts it only if it re-encodes to the same
  string, at most 2,400 characters. Other recipient types are refused.
- The fingerprint is SHA-256 over that exact string, unpadded base64url.
- The body is one age file to that recipient. For a file, the filename is a
  second age file to the same recipient (UTF-8, 1..1024 bytes before
  encryption). Unknown MIME types download as `application/octet-stream`; no
  plaintext filename or MIME type is sent.
- `size_bytes` is the exact age file length, the same unit secrets use. The
  inline path sends the file as canonical standard base64 in `ciphertext`; the
  large path PUTs those raw bytes to the object store.

The envelope beside the body is:

```text
version: 2
encrypted_filename: absent for text; for file, canonical standard base64 of an
                    age file of at most 4,096 bytes
```

Serialized envelope JSON is at most 8,192 bytes. The API rejects unknown
versions and fields, duplicate or case-alias keys, nulls, malformed or
noncanonical base64, a body or filename that does not start with the age header
line `age-encryption.org/v1\n`, wrong lengths, negative or fractional sizes, and
kind/filename mismatches, before any database write. It cannot check anything
inside an age file; only the requester's decryption authenticates the content.

Each request has its own identity, so a body encrypted for one request cannot be
decrypted under another. Unlike the first version, the request ID and kind are
not bound into the ciphertext. The server could therefore relabel a submission
as `text` or `file`, which changes only how the requester's browser presents
the decrypted bytes. Secrets carry `kind` the same way.

## Browser crypto module (#289)

The browser builds on `web/src/lib/crypto/age.ts`, the same module send links
use. The requester page generates the identity with `generateSecretIdentity`
and derives the recipient for creation. The submitter page encrypts the body as
a stream (64 KiB chunks) into a Blob and hashes the age bytes for the large
path's `ciphertext_sha256`. The requester page opens the response body as a
stream with `decryptWith`, which authenticates every chunk and fails on a
truncated file instead of showing a partial one.

Callers bound plaintext with the advertised `/api/config` limits, which are
already plaintext sizes. The browser converts a plaintext length to the age
file length with the size formula in `contracts/age-format-v1.md` to choose
between the inline and the large path.

## Browser flow (#206, #207 and #289)

`/request` creates a request; `/r/{id}` submits text, one file, or a structured
credential (serialized as `FLCR1:` text, so its request kind stays `text`);
`/r/{id}/receive` checks requester status, cancels, or opens once. The main send
page links to request creation.
`web/src/lib/api/requests.ts` sends only the selected capability in Authorization,
uses empty bodies for Open/revoke, and snapshots each encrypted submission body
with its generation and random attempt token for exact retries.

`web/src/lib/state/request-links.ts` parses role-specific fragments and holds one
transient submission-link handoff. Only submission links get native Share and QR
controls. The private retrieval link has a separate copy action and explicitly
warns about key loss, browser history/sync, clipboard managers, and screenshots.
A private link without its identity still permits status/cancellation; it cannot
enable Open. The full private link supports reload and another device, but cannot
recover the separate submission link or a consumed response.

Owner polling starts at ten seconds, runs only while visible, backs off to at most
120 seconds on failures, and stops at terminal states or expiry. Cancellation
refreshes status before confirmation and reads server status after a 409 race.
An unknown Open result never triggers another Open. Explicit 429/503 rejections
allow another user-triggered Open; the API contract says neither consumes content.
The requester verifies the actual `/api/config` limits before enabling Open;
failed or malformed configuration cannot silently substitute a smaller default
and consume a file that the browser would then reject. Plaintext and file object
URLs are cleared when leaving or reaching known expiry. A persisted `pageshow`
reloads cleared request pages without replaying Open or submission. The submission
page explains that its previous in-memory attempt was discarded.

Submitters check the fragment fingerprint before encryption and verify configured
limits before submitting. Files use the advertised `maxFileBytes`, which the API
already clamps to the inline allowance when object storage is disabled.
`payloadInlineMaxBytes` is the plaintext allowance after age overhead (1,044,208
by default); text and files within it use the inline path. The API accepts a
large upload of any file size, so a file never gets stranded between the two
paths. Larger files keep an immutable ciphertext Blob, checksum and attempt
snapshot, reserve an upload, PUT the ciphertext with byte progress, and
finalize. A complete PUT is not acceptance. Only a successful submit/finalize or
matching accepted receipt confirms acceptance.

After an unknown response, the user checks the same attempt and can explicitly
retry its exact encrypted bytes. Cancellation must abandon that reservation and
confirm a new generation before offering a new submission. If the reservation
response was lost, cancellation first resolves the same immutable reservation;
it cannot assume that aborting an HTTP request removed server state. An expired
upload instruction must be abandoned before a new attempt. Leaving/reloading
discards the retry receipt. No identity, plaintext, or filename is written to
browser storage, Svelte navigation state, HTTP, or error messages.

## Lifetime and transitions

Use the existing configured min/default/max TTL and size limits. Defaults are
300/3600/604800 seconds, a 1,048,576-byte inline age file limit
(`FLICK_PAYLOAD_INLINE_MAX_BYTES`), and a plaintext file limit
(`FLICK_MAX_FILE_BYTES`). A large upload may be up to the largest age file that
plaintext limit produces (`secrets.MaxCiphertextBytes`). The single deadline is
creation time plus TTL: submitting never extends it. Owner metadata and token
hashes expire at that same deadline; an API reaper removes them. Backup/WAL
residual risks still apply.

| Current state | Operation | Result |
| --- | --- | --- |
| waiting | Valid inline submit | submitted, atomically stores one envelope and payload |
| waiting | Large reservation | uploading, one attempt and object key reserved |
| uploading | Correct attempt finalize, verified object | submitted |
| uploading | Reservation deadline | waiting if request remains valid; old attempt permanently invalid |
| submitted | Owner open | consumed; inline bytes removed in the same transaction, object cleanup queued when its stream ends |
| waiting/uploading/submitted | Owner revoke | cancelled; remove inline bytes and enqueue object cleanup atomically |
| any live state | Request deadline | unavailable, never reopen |
| consumed/cancelled | Repeated revoke | consumed conflict / cancelled idempotent success |

Transitions use conditional writes in API-owned SQLite transactions. The first
committed accepted submission or open/revoke wins. A submitter cannot replace
an accepted payload, reset TTL, or cancel an accepted submission. Every lookup
checks the deadline even when the reaper is delayed. No status claims a human
has read the data: consumed means the server authorized ciphertext release.

Every request has a monotonically increasing integer `generation`,
starting at 1 and returned in submission instructions. Every submission,
reservation, finalize, abandon, and attempt-status body must carry that value.
The API accepts only the current generation; expired or abandoned reservations
increment the generation atomically before returning to waiting. Permit at most
16 generations per request to bound reservation churn; after generation 16 ends,
the request becomes unavailable instead of accepting another attempt or wrapping.
The UI asks for a new request link when that budget is exhausted. A delayed generation
1 upload cannot become a new attempt after generations 1 and 2 have ended.
No list of old attempt hashes is retained; stale-generation status is unavailable.

Every submission also has a client-generated random 32-byte `attempt_token`, known
before the first request and sent in the JSON body only where specified below.
The API stores the current generation, token hash, and a digest over the immutable envelope,
kind, size, and inline ciphertext (or expected large-object checksum). This is
a bounded retry receipt, retained only to request expiry. Duplicate submission
with the same token and exact content returns the previous outcome without
another transition; a different body/token conflicts. A submitter may query its
attempt status using submission authorization and the attempt token in a POST
body after a lost response. Status returns no ciphertext or owner token.
Clients retry only that exact immutable generation and attempt; they never re-encrypt
or create a new attempt automatically after an unknown outcome.

Creation response loss cannot recover server-issued tokens. Explain the unknown
outcome and let the unused request expire; never automatically create again.
Open response loss is likewise unrecoverable by design.

## HTTP contract

All endpoints below are implemented. `contracts/openapi.yaml` defines the strict
request and response shapes, including reserved large-file uploads.

All paths below are under `/api/requests`. Authorization failures, unknown IDs,
and expired requests return the same `404 request_unavailable`; malformed input
returns `400 invalid_request`, size violations `413 payload_too_large`, and
conflicting state/attempt `409 request_conflict`. Temporary storage failures
return `503 storage_unavailable` and do not consume or finalize.

| Method and path | Authorization | Request / response |
| --- | --- | --- |
| POST `/` | None; create rate limit | public_key, ttl_seconds → id, expires_at, submission_token, retrieval_token |
| GET `/{id}` | Submission token | public_key, fingerprint, expires_at, generation, can_submit; no ciphertext |
| POST `/{id}/submit` | Submission token | generation, attempt_token, kind, size_bytes, envelope, ciphertext → submitted receipt |
| POST `/{id}/attempt` | Submission token | generation, attempt_token → waiting/uploading/accepted/unavailable; no owner details |
| GET `/{id}/owner` | Retrieval token | public_key, fingerprint, expires_at, state; no ciphertext |
| POST `/{id}/open` | Retrieval token | no body → the age file as `application/octet-stream`; consuming |
| POST `/{id}/revoke` | Retrieval token | no body → cancelled; consumed returns conflict |
| POST `/{id}/upload` | Submission token | generation, attempt_token, immutable file metadata and ciphertext checksum → upload instruction, reservation_expires_at |
| POST `/{id}/finalize` | Submission token | generation, attempt_token → submitted receipt |
| POST `/{id}/abandon` | Submission token | generation, attempt_token → waiting with incremented generation; only cancels that pending reservation |

Inline creation accepts omitted `ttl_seconds` as the configured default; an
explicit zero is invalid. Open and revoke require an empty HTTP body. Query
parameters, duplicate JSON fields, case aliases, unknown fields, and null values
are rejected. Successful submit returns `{generation, state: "submitted"}`;
attempt status returns `{generation, state: "waiting"|"uploading"|"accepted"|"unavailable"}`.
Only the matching active attempt sees `uploading`; other attempts see `unavailable`.
Revoke returns `{state: "cancelled"}`. The accepted receipt survives open/revoke
until expiry and describes historical acceptance, not present deliverability.
Immutable content comparison ignores JSON whitespace and property order.

Open answers like a secret open. The body is the raw age file; `Content-Length`
is its exact length, `Flick-Kind` is `text` or `file`, and a file carries
`Flick-Encrypted-Filename` (base64 of the filename age file). The request is
consumed before the first body byte, so a transfer that breaks midway loses the
submission; the requester's decryption then fails at the missing final chunk.
An object-store body is opened before the consuming transaction, so a bucket
that cannot serve it leaves the request openable. A stream may last at most 30
minutes (`requests.OpenStreamTimeout`).

Inline open/revoke delete payload bytes and encrypted metadata in their SQLite
transaction, so no external cleanup job remains. Every write transaction checks
the deadline after acquiring the writer and before commit. Failed COMMIT rolls
back on the same pinned connection before the connection can be reused.
The existing API reaper purges request rows and cascading payloads in bounded
batches, including consumed/cancelled requests, at the original deadline.

Use existing create/open rate-limit configuration for issuance and mutating
submission/owner operations. Metadata/owner/attempt reads must also have bounded
per-client limits. Inline metadata, attempt, submit, open, and revoke share one
per-client bucket across request IDs; rotating IDs does not bypass the limit.
The browser polls owner status at most once per 10 seconds
while visible, stops at terminal state/expiry, backs off on errors, and offers
a manual refresh. No WebSocket or notification service is added.

## Large upload and cleanup (#207)

`/upload` accepts a strict object containing `generation`, `attempt_token`,
`kind: "file"`, `size_bytes`, `envelope`, and `ciphertext_sha256` (canonical
standard base64 of the 32-byte SHA-256 digest of the age file). `size_bytes` is
the exact age file length: at least the 22-byte age header line and at most the
largest age file a maximum-size file produces. Any file may take this path. The
HTTP body is bounded to 16,384 bytes. Disabled S3 returns
`503 storage_unavailable`; advertised file limits already clamp to the inline
maximum when S3 is disabled.

A reservation returns `200 {generation, state: "uploading", upload,
reservation_expires_at}`. `upload` has the existing signed PUT instruction shape;
its `expires_at` equals `reservation_expires_at`. An already accepted identical
retry returns `200 {generation, state: "submitted"}` without another instruction.
`/finalize` and `/abandon` accept only `{generation, attempt_token}` (1,024-byte
HTTP body bound). Finalize returns the submitted receipt. Abandon returns
`{generation: <next>, state: "waiting"}`; ending generation 16 returns
`404 request_unavailable`. Owner status includes `uploading`, but exhaustion
returns 404 for owner/instructions/attempt rather than a new public state.

Reservation lasts at most the existing 15-minute pending-upload TTL
and never past request expiry. Only one reservation is active. Inline submitters
and other large attempts conflict while reserved. The same attempt can recover
an upload instruction after response loss, without extending either deadline;
an expired reservation is invalidated and advances the generation atomically
on the next authenticated operation or reaper tick. An abandon of an older
generation returns 404. If an aborted reserve response has an unknown outcome,
a same-generation `waiting` status does not prove the original call cannot still
reserve: recover the same immutable `/upload` before abandoning it. Do not
discard attempt material or generate a new attempt until a newer generation or
terminal result establishes the outcome.

Object keys are unique, never reused, and exclusively under `managed/requests/`.
Each attempt has its own key. Pin the age file length in the signed PUT and
store the expected SHA-256 checksum with the reservation. The checksum is
integrity metadata, not an access token.
An old issued PUT must never overwrite the immutable accepted payload: finalize
streams the uploaded bytes into a distinct server-only final key while hashing
them, and accepts that final key only if the bytes have the reserved length and
checksum, start with the age header line, and the attempt remains the active
reservation. The check runs on the spooled copy before anything is written, so
a losing finalizer that read a re-PUT upload cannot overwrite the accepted final
object. Finalize holds no body in memory; the object client spools the copy to a
temporary file so the PUT carries a plain `Content-Length`
(`internal/storage/object.go`). Each running finalize therefore uses up to one
object's size of the API container's temporary disk (`/tmp`, no separate limit). No PUT instruction targets a final key. There is
exactly one stable final key per immutable attempt, reused by same-attempt
finalize retries. Reserve the final key in the active attempt record before the
server write, so reconciliation protects both that final key and the upload key
until the attempt commits or becomes invalid. Because the checksum is computed
over the very bytes written to the final key, another PUT replacing the upload
key mid-copy cannot slip unverified bytes into the final key.
A duplicate finalize returns the accepted receipt if that same attempt already
won. Concurrent writes to the final key must use the identical bytes verified
against the immutable attempt checksum. A losing call, timeout, or failed PUT
response must never itself delete or enqueue deletion of the final key: another
call may have accepted it, or the failed response may follow a successful write.
Only API-owned cleanup/reconciliation may schedule deletion after a transaction
proves the key has no active or accepted reference and its generation cannot be
accepted again. Submitted keys remain protected until open/revoke/expiry; active
attempt keys remain protected until their attempt/request deadline or abandon.
Check request expiry again when committing after object verification.

An opened object keeps its `final_key` on the consumed row, stamped with
`consumed_at`, while its body streams. When the stream ends, the API clears the
key and queues the delete (`Store.FinishOpen`). If that step never runs (the API
stops mid-stream), reconciliation and the expiry purge leave the object alone
for 35 minutes after `consumed_at`, longer than the 30-minute stream limit, and
then reclaim it.

Immediate unavailability does not mean immediate physical deletion. Revoke,
open, expiry, reservation expiry, and abandon enqueue ID/key-only cleanup using
the existing outbox/worker ownership boundary. Reconciliation paginates only
`managed/requests/`, checks live API-owned references, and repeats indefinitely
so even PUTs completing after token/metadata removal are reclaimed. Do not
assume presigned URL expiry bounds an already-started upload. Sender cleanup
under `managed/secrets/` must never inspect/delete request objects. Provider
versioning, retention locks, backups, and downtime remain documented residual
risks; no lifecycle rule is silently assumed configured.

## Ownership and implementation boundaries

Use an API-owned `requests` table for recipient, token hashes, state, expiry,
current generation and attempt receipt/hash, immutable envelope, and storage reference, and a separate
inline payload table with cascading deletion. Keep payload-independent owner
metadata only until the original deadline. The API alone writes these tables;
the worker continues processing cleanup through existing internal interfaces.
`internal/db/migrations.go` and `contracts/openapi.yaml` define the implemented
schema and HTTP shapes. `internal/db/requests.go` drops a `requests` table that
lacks `consumed_at`, with its payloads: such a table holds RSA-era submissions
that no client can open any more. Objects those rows referenced are reclaimed by
reconciliation. Request storage references, reservation deadlines, and
independent reconciliation cursor/claims are API-owned.

Tests cover missing/cross-role tokens, substituted recipients, modified
envelopes, duplicate/conflicting submits, parallel opens/revokes, expired
requests, A-expired/B-abandoned/A-delayed reservation replay, the generation
upper bound, simultaneous same-attempt finalize, final PUT response loss
followed by retry (an accepted final key must survive every losing call), and
an opened object that stays protected for the stream grace.
