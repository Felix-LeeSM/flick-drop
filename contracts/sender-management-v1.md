# Sender Management Contract v1 — Planned

Status: accepted design for [#199](https://github.com/Felix-LeeSM/flick-drop/issues/199),
not implemented. [#200](https://github.com/Felix-LeeSM/flick-drop/issues/200),
[#201](https://github.com/Felix-LeeSM/flick-drop/issues/201), and
[#202](https://github.com/Felix-LeeSM/flick-drop/issues/202) implement the API,
cancellation/cleanup, and UI. `openapi.yaml` describes the running API; add the
endpoints there with their implementation, not in this documentation PR.

## Capability and browser custody

For each new secret, the API generates 32 random bytes using `crypto/rand` and
returns their unpadded base64url encoding as `management_token` once, in the
successful create response. Store only SHA-256 of the decoded bytes; reject
noncanonical encodings and compare hashes in constant time. The token is
independent of the secret ID, access proof, and encryption key. Do not derive
one capability from another. Failed creation must not issue a usable token.

The private browser URL is `/m/{id}#manage={management_token}`. The browser
extracts the fragment and sends the token only in the management API's
`Authorization: Bearer <token>` header over HTTPS. Never put the token in a
path/query, recipient URL, NATS event, log, metric, trace, or error report.
Management responses and token-bearing create responses use
`Cache-Control: no-store`. The existing `Referrer-Policy: no-referrer` also
applies to management pages. CORS allows `Authorization` only for the configured
web origin; tokens do not use cookies or implicit browser credentials.

The management URL contains no encryption key or access proof. It cannot
substitute for Model A's access proof or recover Model B's fragment key. Model B
already permits ciphertext release using only the ID; management adds no new
authorization barrier to that existing recipient API. Knowing a management
link therefore does not grant decryption, but can disclose delivery status and
cancel an unopened delivery. No token rotation, recovery, account, or history
endpoint is added. Anyone with the private link has its authority until expiry.

After successful creation, navigate to the management URL. Large-file success
means PUT and `/finalize` both succeeded. Carry the complete recipient URL into
the page through browser memory keyed to that delivery, and clear the handoff
when leaving the management flow. Do not place the handoff in `history.state`,
localStorage, sessionStorage, IndexedDB, the management URL, or the server.
Clear the source form's content, filenames, and passphrase as the current create
flow does. A failed/aborted creation must not show a successful management page.

The initial management view offers recipient-link copy, device sharing, and QR
using that in-memory URL, including Model B's `#key=...`. Copying/saving the
private management link is a separate, explicitly labelled action. Never share
`window.location.href` as the recipient link. Explain: keep the management link
private; a lost link cannot be recovered.

A reload, direct visit, or another device gets status/cancellation only, for
both access models. Do not reconstruct even a Model A recipient link in this
view: the consistent message is "The recipient link is available only in the
original creation session. Use the copy you saved or create a new delivery."
A management visit must never call the recipient `/open` endpoint. No persistent
browser history of deliveries is introduced.

## Proposed HTTP surface

All field names below belong to proposal v1. Existing create request fields,
recipient routes, encryption formats, and finalize request/response stay valid.
Both inline and staged-S3 create responses add:

```json
{
  "management_token": "<one-time-returned-base64url-token>",
  "management_expires_at": "<the-original-expires_at>"
}
```

These are additive fields beside the existing `id`, `expires_at`, and optional
`upload`. The browser must preserve the fields across PUT/finalize;
`web/src/lib/api/secrets.ts:createLargeFileSecret` currently discards extra
fields. Existing CLI decoding in `internal/flickcli/client.go:Client.do` accepts
unknown response fields. Existing secrets receive no retroactive token, and
legacy clients need no new commands or request fields.

| Request | Success | Purpose |
| --- | --- | --- |
| `GET /api/secrets/{id}/management` | `200` status snapshot | Read without consuming |
| `POST /api/secrets/{id}/revoke` with `{}` | `200` cancelled snapshot | Cancel active/pending delivery |

Both requests require the bearer token for that ID. The snapshot contains only
`id`, `status`, `expires_at`, `management_expires_at`, and `can_cancel`.
The two expiry fields are equal and never extend on reads or cancellation.
No response contains ciphertext, encrypted filenames, keys, access proofs, a
recipient URL, or the raw management token again.

Missing, malformed, wrong-ID, wrong, expired, and unknown tokens/records return
the same `404` error code `management_unavailable`, with no status fields.
Authenticated cancellation of `opened`, `locked`, or `unavailable` returns `409`
code `not_cancellable` plus the same safe status snapshot. Repeated cancellation
of `cancelled` returns `200`; it does not schedule duplicate immediate jobs.
Malformed JSON returns `400`; rate limits use `429`; transient failures use
`503` and must not masquerade as a terminal state. Apply the existing
`FLICK_OPEN_RATE_PER_MIN` limit through a separate management limiter instance,
shared by status/revoke; do not spend recipient proof-attempt counts.

Public recipient metadata retains its existing fields and error behavior; do
not add management-only outcomes or management credentials there. Existing
public active/404/410 responses are not promised to conceal all delivery state.

## States, expiry, and minimum metadata

Management authority ends at the original secret `expires_at`, including after
an early open, lockout, cancellation, or abandoned upload. No extra history
window or configurable management TTL is introduced. Authorize with server UTC
and `now < expires_at`; at the boundary, the uniform unavailable response wins.

| Status before management expiry | Meaning | `can_cancel` |
| --- | --- | --- |
| `pending_upload` | Staged object not finalized, pending deadline not reached | `true` |
| `active` | Available for one-time opening | `true` |
| `opened` | The verified open transaction committed permission to release ciphertext | `false` |
| `locked` | Five invalid access proofs exhausted access | `false` |
| `cancelled` | Sender cancellation won before opening | `false` |
| `unavailable` | Pending upload was reclaimed without activation | `false` |

`opened` does not prove HTTP delivery, decryption, reading, or a recipient's
identity. An interrupted HTTP response after commit is still `opened`.
`consumed_at` alone is insufficient: current `recordFailedAccessTx` sets it for
lockout as well as `OpenTx` for a successful open.

Add an API-owned `secret_management` record for new deliveries containing only
`secret_id`, `token_hash`, the original `expires_at`, and nullable `outcome`
(`opened`, `locked`, `cancelled`, or `unavailable`). Create the record in the
same transaction as the secret. There is no cascade from payload/secret-row
deletion to management metadata: early cleanup must not erase the sender's
outcome. Pending/active come from the live secret row; record terminal outcomes
in the same transaction as the domain transition, before payload cleanup.
An elapsed pending-upload deadline reads as `unavailable` immediately, even
if the reaper has not yet persisted that outcome. An unexpected missing live
row without an outcome is a server error, not a fabricated open/cancellation. Existing rows without a token keep their old flow.

Payload deletion and object cleanup keep their existing immediate/asynchronous
boundaries. A reclaimed pending upload gets `unavailable` and loses its payload
at `min(created_at + PendingTTL, expires_at)`. Terminal managed secret rows may
be removed after their required object cleanup has been durably enqueued;
retain only the separate management record until its expiry. The API reaper
purges expired management records, including their hashes, on its next bounded
sweep even when `consumed_at` is set. No consumed-row exclusion may skip this
purge. There is no retention extension during API outages: authorization still
fails at the original deadline; deletion resumes when the reaper resumes.

A page already holding an authenticated snapshot can count down locally and
show "Management link expired" at its known deadline, then discard its details
and stop polling. A fresh expired visit shows "Management link unavailable"
because authentication can no longer distinguish expiry from an invalid link.
Use a refresh action plus foreground polling no faster than once per 30 seconds;
stop polling on terminal states/expiry and on hidden pages, resume on focus.
Network errors keep an explicit unknown/stale state and a retry action.

## Cancellation transaction and races

Authenticate first, then condition the mutation on current unexpired,
unconsumed `active` or `pending_upload` state. Commit cancellation outcome,
blocking of recipient access, inline payload removal, and required outbox
records in one `api.db` transaction. Enqueue existing `delete_secret` and/or
`delete_oci_object` jobs only where needed, with reason `manual`. A failed enqueue
or commit must roll everything back. An acknowledged cancellation is irreversible.

`OpenTx`, proof lockout, cancellation, expiry, and `Finalize` must use compatible
conditional mutations and affected-row checks. Exactly one terminal transition
wins. A completed open prevents cancellation; completed cancellation prevents
all later open/finalize success, including a finalize already performing HEAD.
Recheck state and expiry at the mutation, not only before network I/O. Do not
change a cancellation into `opened` merely because an object still exists.
Keep pending-upload accounting balanced once per actual state transition.

Worker deletion is asynchronous and idempotent under existing retries and
dead-letter handling. Cancellation does not revoke ciphertext already released,
erase recipient copies, or guarantee physical erasure from storage/backups.

## Late PUT cleanup without a completion deadline

A presigned PUT is not revoked by an API state change. URL expiry limits request
authorization, not a proven upper bound on completion of requests already
accepted. Immediate DELETE, HEAD returning absent, or a scan with no result
cannot prove that an object will not appear later. No finite upload-completion
grace period is assumed.

For new managed S3 deliveries, use the exclusive object-key prefix `managed/secrets/`
and a fresh, never-reused `managed/secrets/{id}` key. The prefix belongs only to
outbound sender secrets; M9 request objects must use a separate namespace such
as `managed/requests/` with their own live-row check. Other application data and
legacy keys must stay outside the sender prefix. Store the full key in the
normal secret row. Issue an upload instruction only after the row and management record
commit, so a legitimate upload cannot precede its live-row protection.

Extend the API expiration reaper with recurring paginated object reconciliation:

1. Use `ListObjectsV2` only for `managed/secrets/`. Process at most the existing
   reaper batch size per tick. Persist the next listing cursor in API-owned state,
   advance it only after the batch commits, restart at the beginning after a
   full pass, and reset invalid cursors safely. Do not let ordinary expiry or
   the first listing page starve later pages; a restart must resume progress.
2. Before enqueueing deletion, the API transaction checks for a matching live,
   unconsumed secret row with the same storage key. Protect active rows before
   `expires_at` and pending rows before their pending/expiry deadline. Do not
   treat a DB error as "no live row". No worker writes to `api.db`.
3. Otherwise enqueue `delete_oci_object` through the existing outbox. Keep one
   outstanding reconciliation job per key in API-owned bookkeeping. After
   persisting a terminal receipt, the worker calls a job-ID-fenced internal API acknowledgement
   before acknowledging/terminating NATS delivery. The endpoint clears only that
   job's pending claim; a repeated acknowledgement succeeds harmlessly. Duplicate
   deliveries of terminal receipts must repeat this acknowledgement too, without
   rerunning the delete. API failure retries acknowledgement, including after a
   crash; do not time out a live claim.
4. A completed or dead-lettered job is not a permanent exclusion. If the object
   appears in a later pass, create a fresh job ID; reusing a succeeded ID would
   make the worker skip a late PUT. Remove completed bookkeeping, leaving no
   permanent per-delivery tombstones. At most one pending cleanup claim per key
   survives an outage, and an unhealthy cleanup backlog needs operator recovery.

The object namespace, durable cursor, and recurring scan survive removal of
management metadata. Thus an object that finishes arbitrarily late can still be
found and deleted on a later complete pass. This is eventual cleanup while the
API, listing permission, broker, worker, and object store are operating, not a
wall-clock deletion SLA. Scans do not run on `/metrics` requests. Provider
lifecycle rules may add a backstop but are not assumed enabled or relied upon.

The managed prefix requires List permission alongside existing Get/Put/Delete
permissions. Verify listing against the configured provider; failures must be
visible and retried rather than interpreted as an empty bucket. The guarantee
covers removal of the current object, not historical versions, replicas, or
backups. Retention/object-lock can prevent deletion until the operator's policy
permits it; retries/dead letters must not be reported as successful erasure.
Operators own those retention policies. Do not silently change bucket settings,
assume versioning/locks are disabled, or introduce a new startup/provisioning
gate that disables existing S3 operation.

[S3 presigned URL documentation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html)
describes request-time expiry checks.
[Oracle's S3 SDK example](https://docs.oracle.com/en/learn/ocios-s3-api-cpp/)
uses `ListObjectsV2` against OCI; integration must still verify the configured
provider and permissions rather than infer them from an S3-compatible label.

## Implementation verification

- Migrate a populated current DB and an empty DB; legacy recipient/CLI flows
  remain compatible. Test hash-only persistence, malformed/wrong/expired tokens,
  separate proof authority, CORS, no-store, and absence of secrets in telemetry.
- Check all state rows for inline/S3 and Model A/Model B, before/after cleanup;
  cover the exact expiry boundary and pending deadline, including consumed rows.
- Race open/revoke, lockout/revoke, finalize/revoke, and expiry/revoke; force
  outbox failure to prove rollback and duplicate requests to prove idempotency.
- With MinIO, delete a cancelled object, finish another valid PUT afterward,
  purge management metadata, restart scanning mid-pagination, and prove that a
  fresh worker job deletes the late object. Also test a simulated PUT completing
  after signature expiry without assuming the provider closes its connection.
- Fail listing, DB lookup, enqueue, delete, and terminal acknowledgement; verify
  bounded pending claims, recovery, pagination fairness, and no deletion of live
  rows, legacy keys, or keys outside `managed/secrets/`.
- Browser checks cover initial recipient sharing, Model B fragment preservation,
  refresh/new-device management, no persistent key handoff, invalid/expired URLs,
  locked versus opened copy, and cancellation races. Inspect requests/storage;
  management tokens and recipient keys must never cross their defined boundaries.
