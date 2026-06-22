# `internal/secrets/` Guide

This package owns the secret lifecycle domain: create (inline and large),
fetch metadata, consume, expire, and cleanup decisions.

## Lifecycle

A secret moves through these states:

- `active`: ciphertext present, openable until `expires_at`.
- `pending_upload`: a large secret staged by `CreateLarge` (`store.go:264`)
  awaiting `Finalize` (`store.go:355`). The browser uploads ciphertext to S3
  via a presigned POST, then calls `/finalize`, which HEADs the object and
  flips the row to `active` (`activateSecretTx`, `store.go:414`).
- reaped: hard-deleted by the expiration reaper (see below).

Small payloads (≤ inline threshold) write a SQLite BLOB inline (`Create`,
`store.go:160`; `StorageSQLite`, `store.go:20`); large payloads write an S3
object (`CreateLarge`; `StorageS3`, `store.go:21`). Both paths share the same
`secrets` row shape, differing only in `storage_backend`. Core types:
`KDFParams` (`store.go:26`), `CreateInput`, `CreateLargeInput`/`CreateLargeResult`
(`store.go:52,65`), `Secret` (`store.go:71`), `Metadata` (`store.go:87`).

## Expiration reaper

`reaper.go` owns expiry and orphan reclaim. `Reaper` (`reaper.go:60`) runs on an
interval and, per batch, claims reclaimable rows atomically via
`claimReclaimableSQL` (`reaper.go:33`): an `active` row past `expires_at`
(active expiry) or a `pending_upload` row past `created_at + PendingTTL`
(`store.go:431`; orphan reclaim — uploads that never called `/finalize`). The
claim sets `reclaim_enqueued_at` so multiple instances do not double-claim; rows
are ordered by a unified "reclaimable-since" timestamp so orphans are not starved
by an active-expiry backlog. Reaped secrets are hard-deleted (ciphertext +
metadata); for S3-backed rows an outbox event is enqueued for object-storage
cleanup (SQLite-backed rows need no object cleanup).

## Access attempts

Invalid access-proof attempts increment a server-side counter without storing
passphrases or decrypt keys. After `maxFailedAccessAttempts` (5, `store.go:23`)
the secret is marked consumed and its payload removed.

Directory structure:

- Keep lifecycle flows readable and close together.
- `store.go` owns create/open/consume/large-secret SQL and types; `reaper.go`
  owns expiry and orphan reclaim; `errors.go` owns sentinel errors.
- Do not create generic utility folders.

Rules:

- The domain must assume the server never knows plaintext, passphrases, or
  derived keys.
- Share links expose only secret IDs.
- `Finalize` must guard the pending→active flip against the reaper race: 0
  affected rows means the row was reaped, so treat the upload as reaped, not
  finalized (`activateSecretTx`, `store.go:414`).
- Deletion semantics must match the documented residual-risk model.
- Storage, events, and database details should enter through explicit
  dependencies (`Store` holds `*sql.DB` + `storage.ObjectStore`; `Reaper` takes
  an `outboxEnqueuer`, `reaper.go:56`), not hidden globals.
