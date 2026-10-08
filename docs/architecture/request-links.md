# One-time request links v1

Status: approved v1 contract from #203. #205 implements the inline API and
SQLite lifecycle. Browser crypto/UI and large-object reservations are separate
#204/#206/#207 deliveries. Existing send-link formats and CLI vectors are
unchanged. Sender management follows the separate #199 contract.

## Product and authority

A requester creates one short-lived request and saves a private retrieval link.
The requester shares a separate submission link. The first accepted submission
wins; the requester can release its ciphertext once and decrypt locally. There
are no accounts, repeat collection, permanent inboxes, or recovery service.

The browser generates a fresh RSA key pair before creation. The API receives
only the public key and issues independent 32-byte random submission and
retrieval tokens. Both use unpadded base64url; the API stores SHA-256 hashes,
never raw tokens. Tokens are unrelated to key material and the request ID.

| Capability | Read submission instructions | Submit | Read owner status/revoke | Release ciphertext | Decrypt |
| --- | --- | --- | --- | --- | --- |
| ID alone | No | No | No | No | No |
| Submission token and public-key fingerprint | Yes | Once | No | No | No |
| Retrieval token | No | No | Yes | Once | No |
| RSA private key alone | No | No | No | No | Only with ciphertext |
| Private retrieval link (token and key) | No | No | Yes | Once | Yes |

Submission URL: `/r/{id}#submit=<token>&fp=<fingerprint>`.
Private retrieval URL: `/r/{id}/receive#receive=<token>&key=<pkcs8>`.
Fragments never enter HTTP requests. The appropriate token travels only in
`Authorization: Bearer <token>`, never in a query or path. Cross-role tokens
fail authorization. Apply `Cache-Control: no-store` to all request API responses,
allow Authorization only for the configured web CORS origin, and exclude
headers, bodies, URLs, tokens, and key material from logs and telemetry.

The submission fragment's fingerprint pins the public key against accidental
or server-side substitution when the client code is honest. Whoever can replace
the shared link can replace its fingerprint. A malicious server can still serve
malicious JavaScript; this browser limitation is the same as ordinary send links.
No requester identity or submitter identity is authenticated.

## Key custody

Only the requester browser generates or imports the RSA private key. Export it
as PKCS#8 for the private retrieval fragment; never transmit it to the API.
Keep working keys and decrypted data in memory. Do not put them in
localStorage, sessionStorage, IndexedDB, analytics, or Svelte history state.

The private retrieval URL deliberately contains the key, unlike an ordinary
sender management URL. Saving or reopening that complete private URL supports
refresh, browser restart, and another device while the request is valid. The
UI must explain that clipboard managers, browser history/sync, screenshots, and
anyone receiving the link may retain the complete retrieval capability. Do not
promise the URL is absent from history. A fragment-free URL cannot recover it.
Losing the complete link/key means the submission cannot be recovered.

On the retrieval page, import and validate the private key before enabling
Open. Derive its public key and verify the stored public-key fingerprint before
the consuming request. Status reads do not consume. After consumption, a
refresh cannot retrieve again; retain the plaintext/download only in memory.
Do not automatically open, replay an open, or retry a lost open response.

## Browser encryption envelope

Use only native Web Crypto primitives in a secure context: RSA-OAEP with
SHA-256/MGF1-SHA-256, a 2048-bit modulus, and exponent 65537; AES-256-GCM with a
128-bit tag. RSA encrypts only a fresh 32-byte AES content key, never the file.
This uses the [Web Crypto RSA-OAEP operations](https://www.w3.org/TR/webcrypto/#rsa-oaep)
and [AES-GCM operations](https://www.w3.org/TR/webcrypto/#aes-gcm).
If these operations are unavailable, show an unsupported-browser error before
creation/submission; do not downgrade or add a custom primitive.

Public keys use DER SPKI encoded as canonical standard base64. The fingerprint
is SHA-256 over the SPKI bytes, encoded unpadded base64url. Validate DER RSA
public key type, modulus length, and exponent in both API and browser. Private
fragments use canonical unpadded base64url DER PKCS#8; bound the encoded key to
4,096 characters before decoding and reject other algorithms/sizes.

Each submit attempt generates a new AES key and independent random 12-byte
payload and filename nonces; reject equal nonces. There is no passphrase/KDF.
For request ID `id` and kind `text` or `file`, byte-exact UTF-8 bindings are:

```text
RSA-OAEP label: Flick request v1\n{id}\n{kind}\nkey
Payload AAD:   Flick request v1\n{id}\n{kind}\npayload
Filename AAD:  Flick request v1\n{id}\n{kind}\nfilename
```

Here `\n` denotes one LF byte, with no final newline. IDs permit no newline.
RSA wrapping and AES authentication bind the content to the request and kind.
Unwrapping must return exactly 32 bytes. Check declared plaintext size against
the decrypted byte length before displaying or downloading.

The envelope object has these exact fields (all binary fields standard base64):

```text
version: 1
algorithm: "RSA-OAEP-256+A256GCM"
wrapped_key: 256 bytes
nonce: 12 bytes
encrypted_filename: absent for text; {nonce: 12 bytes, ciphertext: 17..1040 bytes} for file
```

`kind`, `size_bytes`, and envelope are immutable submission metadata. File names
are UTF-8, 1..1024 bytes before encryption; unknown MIME types are downloaded
as `application/octet-stream`. No plaintext filename or MIME subtype is sent.
Payload ciphertext length is exactly `size_bytes + 16`; inline requests include
its base64, while large uploads carry those same bytes to the object store.
Limit serialized envelope JSON to 4,096 bytes. Reject unknown versions,
algorithms, fields, malformed/noncanonical base64, wrong lengths, negative or
fractional sizes, kind/filename mismatches, and invalid key material before
database writes or crypto. Do not reinterpret existing Model A/B envelopes.

## Browser crypto module (#204)

`web/src/lib/crypto/requests.ts` implements this envelope without HTTP, browser
storage, UI, or crypto dependencies. Callers supply explicit plaintext byte
bounds through `RequestLimits.maxTextBytes` and `RequestLimits.maxFileBytes`;
the later API client subtracts the 16-byte GCM tag from its inline ciphertext
limit to obtain `maxTextBytes`. The module returns base64 ciphertext for the inline
path; large-upload callers decode those same bytes for the object-store PUT.
Existing send-link crypto remains separate.

Both key import functions require the expected public-key fingerprint. The
submitter obtains that value from its shared fragment; the requester obtains
it from owner status before consuming Open. Imports check native RSA parameters
and byte-for-byte canonical DER re-export. Private import also verifies a local
OAEP encrypt/decrypt challenge so structurally accepted but unusable private
components cannot enable a consuming Open. Keys and challenge bytes stay in
memory. The module rejects request IDs longer than 256 characters, CR/LF, and
lossy UTF-8; callers use the server-issued ID unchanged.

`tests/fixtures/request-crypto-v1.json` contains immutable public synthetic
RSA material and ciphertext produced independently with Node's `node:crypto`.
Never use these keys for real requests or regenerate the fixture to match a
changed implementation. Unit tests decrypt those vectors, reject an otherwise
valid AES-128 downgrade, and cover malformed keys, tampering and request/kind
replay. Run `pnpm --dir web test` for unit coverage and
`pnpm --dir web exec node --test src/lib/crypto/requests.browser.mjs` for actual
Chromium Web Crypto. The browser harness bundles the module with Vite and serves
it through Playwright's intercepted HTTPS origin, with no listener, API, or
object store. Native RSA-OAEP/AES-GCM and secure contexts are required; the
included browser check targets Chromium, with no Firefox or Safari validation.

## Lifetime and transitions

Use the existing configured min/default/max TTL and size limits. Defaults are
300/3600/604800 seconds, a 1,048,576-byte inline ciphertext limit, and a
52,428,800-byte plaintext file limit. The single deadline is creation time plus
TTL: submitting never extends it. Plaintext text must fit the inline bound
after the 16-byte GCM tag. Owner metadata and token hashes expire at that same
deadline; an API reaper removes them. Backup/WAL residual risks still apply.

| Current state | Operation | Result |
| --- | --- | --- |
| waiting | Valid inline submit | submitted, atomically stores one envelope and payload |
| waiting | Large reservation | uploading, one attempt and object key reserved |
| uploading | Correct attempt finalize, verified object | submitted |
| uploading | Reservation deadline | waiting if request remains valid; old attempt permanently invalid |
| submitted | Owner open | consumed; release once and enqueue cleanup atomically |
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
Clients retry only that exact immutable generation and attempt; they never regenerate an
encryption key or new attempt automatically after an unknown outcome.

Creation response loss cannot recover server-issued tokens. Explain the unknown
outcome and let the unused request expire; never automatically create again.
Open response loss is likewise unrecoverable by design.

## HTTP contract

The first seven endpoints below are implemented for inline content. The
`upload`, `finalize`, and `abandon` endpoints remain unavailable until #207.
`contracts/openapi.yaml` describes only the implemented subset.

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
| POST `/{id}/open` | Retrieval token | no body → one envelope and ciphertext; consuming |
| POST `/{id}/revoke` | Retrieval token | no body → cancelled; consumed returns conflict |
| POST `/{id}/upload` | Submission token | generation, attempt_token, immutable file metadata and ciphertext checksum → upload instruction, reservation_expires_at |
| POST `/{id}/finalize` | Submission token | generation, attempt_token → submitted receipt |
| POST `/{id}/abandon` | Submission token | generation, attempt_token → waiting with incremented generation; only cancels that pending reservation |

Inline creation accepts omitted `ttl_seconds` as the configured default; an
explicit zero is invalid. Open and revoke require an empty HTTP body. Query
parameters, duplicate JSON fields, case aliases, unknown fields, and null values
are rejected. Successful submit returns `{generation, state: "submitted"}`;
attempt status returns `{generation, state: "waiting"|"accepted"|"unavailable"}`.
Open returns `{kind, size_bytes, envelope, ciphertext}` and revoke returns
`{state: "cancelled"}`. The accepted receipt survives open/revoke until expiry
and describes historical acceptance, not present deliverability. Immutable
content comparison ignores JSON whitespace and property order.

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

Large upload endpoints remain unavailable until #207; #205 rejects above-inline
payloads. Reservation lasts at most the existing 15-minute pending-upload TTL
and never past request expiry. Only one reservation is active. Inline submitters
and other large attempts conflict while reserved. The same attempt can recover
an upload instruction after response loss, without extending either deadline;
if the instruction expired, the attempt must be abandoned before a new attempt.

Object keys are unique, never reused, and exclusively under `managed/requests/`.
Each attempt has its own key. Pin ciphertext length in the signed PUT and store
the expected SHA-256 ciphertext checksum with the reservation. Validate actual
bytes against that checksum before accepting finalize. The checksum is integrity
metadata, not an access token.
An old issued PUT must never overwrite the immutable accepted payload: finalize
writes those same verified bytes to a distinct server-only final key, then atomically
accepts that final key only if the attempt remains the active reservation. No
PUT instruction targets a final key. There is exactly one stable final key per
immutable attempt, reused by same-attempt finalize retries. Reserve the final key in the active attempt
record before the server write, so reconciliation protects both that final key
and the upload key until the attempt commits or becomes invalid. Do not validate
a GET then copy a mutable
source key: another PUT could replace the source between those operations.
A duplicate finalize returns the accepted receipt if that same attempt already
won. Concurrent writes to the final key must use the identical bytes verified
against the immutable attempt checksum. A losing call, timeout, or failed PUT
response must never itself delete or enqueue deletion of the final key: another
call may have accepted it, or the failed response may follow a successful write.
Only API-owned cleanup/reconciliation may schedule deletion after a transaction
proves the key has no active or accepted reference and its generation cannot be
accepted again. Submitted keys remain protected until open/revoke/expiry; active
attempt keys remain protected until their attempt/request deadline or abandon.
Check request expiry again when committing after object verification. Finalize may buffer ciphertext
up to the existing file-size cap, as the current open operation already does.

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

Use an API-owned `requests` table for public key, token hashes, state, expiry,
current generation and attempt receipt/hash, immutable envelope, and storage reference, and a separate
inline payload table with cascading deletion. Keep payload-independent owner
metadata only until the original deadline. The API alone writes these tables;
the worker continues processing cleanup through existing internal interfaces.
`internal/db/migrations.go` and `contracts/openapi.yaml` define the implemented
inline schema and HTTP shapes. Inline records have no external storage reference;
#207 adds the reservation states and object references when implemented.

- #204: browser-only crypto module, immutable synthetic decrypt golden vectors,
  tamper/cross-request vectors and strict validation; no HTTP/UI/storage.
- #205: inline API, schema, contracts, auth/state/race/expiry tests; no large
  reservation endpoints or request UI.
- #206: requester/submitter API client and browser flow using #204, two-context
  end-to-end tests, and explicit private-link custody and unknown-outcome UX.
- #207: reserved object uploads, immutable final storage, reconciliation, and
  actual browser/MinIO tests, including delayed PUT and losing finalizers.

All four require independent review. Test missing/cross-role tokens, substituted
keys, modified envelopes, duplicate/conflicting submits, parallel opens/revokes,
expired requests, mismatched private keys, and existing send-link compatibility.
Include A-expired/B-abandoned/A-delayed reservation replay, the generation
upper bound, simultaneous same-attempt finalize, and final PUT response loss
followed by retry; an accepted final key must survive every losing call.
