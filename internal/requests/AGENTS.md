# `internal/requests/` Guide

- `store.go` owns API-only request creation, authorization, inline submission,
  one-time retrieval, and owner cancellation. `large.go` owns object reservation,
  verified finalize, and generation-fenced abandon. The API alone writes the
  `requests` and `request_payloads` tables.
- `internal/db/requests.go:dropRSARequests` drops RSA-era request tables (those
  without `consumed_at`); request links are age-only.
- `validation.go` enforces `docs/architecture/request-links.md`: a canonical
  hybrid age recipient, independent 32-byte capabilities, the version 2
  envelope, and age header magic on every body and filename. `size_bytes` is
  the exact age file length.
- `Store.Finalize` streams the uploaded object into the stable server-only final
  key while hashing it; it never buffers the body. No finalizer may delete or
  enqueue deletion of a final key after a losing transition or unknown PUT
  response.
- `Store.Open` opens an object body before its consuming transaction, then
  rechecks authorization, deadline, state, and final key. Failed opens never
  consume. The caller streams `Opened.Body`, closes it, then calls
  `Store.FinishOpen`, which clears `final_key` and queues the delete. Until
  then, `consumed_at` protects the object for `openObjectGrace`.
- `cleanup.go:PurgeExpiredTx` shares the API expiry reaper transaction and enqueues
  required object jobs with metadata removal or reservation invalidation.
  `Store.ReconcileOnce` scans only `managed/requests/`, protects live request
  references, and owns `request_reconciliation_cursor` and
  `request_reconciliation_pending`. Sender scans remain in `internal/secrets`.
- `Store.AcknowledgeObjectCleanup` clears only matching request key/job-ID claims.
  Every worker must acknowledge both managed namespaces before the API scanner
  is deployed; `docs/architecture/event-contract.md` defines rollout order.
- HTTP handlers live in `internal/httpapi/requests.go`. Never log request
  bodies, tokens, public keys, encrypted filenames, or ciphertext.
