# `internal/requests/` Guide

- `store.go` owns API-only request creation, authorization, inline submission,
  one-time retrieval, and owner cancellation. `large.go` owns object reservation,
  verified finalize, and generation-fenced abandon. The API alone writes the
  `requests` and `request_payloads` tables.
- `internal/db/requests.go:normalizeRequestsSchema` upgrades the request state
  CHECK while preserving existing inline payloads and cascading foreign keys.
- `validation.go` enforces `docs/architecture/request-links.md`: canonical RSA
  SPKI, independent 32-byte capabilities, and the version 1 encrypted envelope.
- `Store.Finalize` writes the bounded, checksum-verified GET bytes to the stable
  server-only final key. No finalizer may delete or enqueue deletion of a final
  key after a losing transition or unknown PUT response.
- `Store.Open` loads object bytes before its consuming transaction, then rechecks
  authorization, deadline, state, and final key. Failed reads never consume.
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
