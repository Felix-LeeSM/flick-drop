# `internal/secrets/` Guide

This package owns the secret lifecycle domain: create (inline and large),
fetch metadata, consume, expire, and cleanup decisions.

## Lifecycle

A secret moves through these states:

- `active`: ciphertext present, openable until `expires_at`.
- `pending_upload`: a large secret staged by `store.go:Store.CreateLarge`
  awaiting `store.go:Store.Finalize`. The browser uploads the age file to the
  object store via a presigned PUT whose signature pins Content-Length
  (`internal/storage/presigned.go:Client.PresignPUT`), then calls `/finalize`,
  which HEADs the object, checks its size equals
  `store.go:CreateLargeInput.SizeBytes` (the exact age file length the client
  announced at create), and flips the row to `active`
  (`store.go:Store.activateSecretTx`).
- reaped: hard-deleted by the expiration reaper (`reaper.go:Reaper`, described
  in the `## Expiration reaper` section of `internal/secrets/AGENTS.md`).
- opened: forgotten. `Store.OpenTx` (`store.go`) deletes the `secrets` row, its
  inline payload (cascade), and its `secret_management` row in the open
  transaction, so every later request for the ID gets the unknown-ID response
  (`ErrNotFound`, `ErrManagementUnavailable`). A bucket body that may still be
  streaming gets a `secret_open_grace` row holding only `object_key` and
  `grace_until` (`openObjectGrace`, `age.go`); `reconciliation.go:protectObjectTx`
  honors it and `management.go:purgeManagementTx` deletes it after the deadline.

Small payloads (≤ inline threshold) write a SQLite BLOB inline
(`store.go:Store.Create`, backend `store.go:StorageSQLite` = `sqlite_blob`);
large payloads write an object-storage object (`store.go:Store.CreateLarge`,
backend `s3_object` or `r2_object` from
`internal/storage/registry.go:ChooseBackend`). Both paths share the same
`secrets` row shape, differing only in `storage_backend`. Core types live in
`store.go`: `KDFParams`, `CreateInput`, `CreateLargeInput`,
`CreateLargeResult`, `Secret`, and `Metadata`.

## Expiration reaper

`reaper.go` owns expiry and orphan reclaim. `reaper.go:Reaper` runs on an
interval (`reaper.go:RunReaper`) and, per batch, claims reclaimable rows
atomically via `reaper.go:claimReclaimableSQL`: an `active` row past
`expires_at` (active expiry) or a `pending_upload` row at the earlier of
`expires_at` and `created_at + PendingTTL` (`store.go:Store.PendingTTL`;
orphan reclaim — uploads that never called `/finalize`). The
claim sets `reclaim_enqueued_at` so multiple instances do not double-claim; rows
are ordered by a unified "reclaimable-since" timestamp so orphans are not starved
by an active-expiry backlog. Reaped secrets are hard-deleted (ciphertext +
metadata); for object-storage rows (`s3_object`, `r2_object`) an outbox event
is enqueued for object-storage cleanup (SQLite-backed rows need no object cleanup).

## Access attempts

Invalid access-proof attempts increment a server-side counter without storing
passphrases or decrypt keys. After `store.go:maxFailedAccessAttempts` (5)
the secret is marked consumed and its payload removed.

Directory structure:

- Keep lifecycle flows readable and close together.
- `store.go` owns create/open/consume/large-secret SQL and types; `reaper.go`
  owns expiry and orphan reclaim; `errors.go` owns sentinel errors.
- `management.go` issues independent random capabilities, stores SHA-256 hashes,
  reads authenticated outcomes without loading payloads, and purges managed
  metadata at the original expiry. `reaper.go:ClaimOnce` calls the bounded purge
  even for consumed (locked) managed secrets; legacy locked-row retention is
  unchanged. Opened secrets keep no outcome: the management record is deleted
  at open, and `opened` is never written.
- `management.go:Revoke` authenticates and commits cancellation with payload
  deletion and the required object-delete outbox record. A failed commit must
  roll back on the exclusively owned SQLite connection before releasing it.
- `reconciliation.go:Reaper.ReconcileOnce` scans only `managed/secrets/`
  (`management.go:managedObjectPrefix`) on each configured backend, keeping a
  cursor per backend, protects live rows, and commits the generation-fenced cursor with per-key pending jobs.
  `Store.AcknowledgeObjectCleanup` clears only the matching key/job-ID claim.
- Do not create generic utility folders.

Rules:

- The domain must assume the server never knows plaintext, passphrases, or
  derived keys.
- Share links expose only secret IDs.
- `Finalize` must guard the pending→active flip against the reaper race: 0
  affected rows means the row was reaped, so treat the upload as reaped, not
  finalized (`store.go:Store.activateSecretTx`).
- Deletion semantics must match the documented residual-risk model.
- Storage, events, and database details should enter through explicit
  dependencies (`store.go:Store` holds `*sql.DB`, a `*storage.Registry`, and an
  `outboxEnqueuer`; `reaper.go:NewReaper` takes an `outboxEnqueuer`, declared
  in `reaper.go`), not hidden globals.
