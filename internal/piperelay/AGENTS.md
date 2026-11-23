---
type: Agent Guide
title: internal/piperelay Guide
description: Boundary, hard security invariants, Valkey/streaming rules, and test prerequisites for the Live Transfer (/api/pipe/*) coordinator package.
tags: [agent-guide, go, live-relay]
---

# `internal/piperelay/` Guide

This package coordinates Live Transfer (`/api/pipe/*`): real-time HTTP pipe transfers between devices that store nothing in PostgreSQL or object storage, served by any number of API replicas. Full protocol, Valkey schema, presence-lease, and pod-to-pod stream details live in `docs/architecture/live-relay.md`.

## Package Layout

- `internal/piperelay/coordinator.go`: `Coordinator` public API (`Init`, `Join`, `Status`, `Wait`, `Select`, `Decline`, `Leave`, `Cancel`, `ActiveSessions`, `Drain`, `Close`).
- `internal/piperelay/room.go`: Valkey room hash (`{Namespace}room:{roomID}`), CAS + Pub/Sub Lua scripts (`casRoomScript`, `createRoomScript`, `acquireLeaseScript`, `releaseLeaseScript`), `roomHub`, `settle`, and `pruneIdle`; `cleanOnce` is in `internal/piperelay/coordinator.go`.
- `internal/piperelay/stream.go`: `Receive`, `Send`, and `Coordinator.InternalHandler` (pod-to-pod stream listener mounted by `cmd/flick-api/main.go` on `FLICK_INTERNAL_HTTP_PORT`, kept in `internal/piperelay` rather than `internal/httpapi` because it is relay transport).

## Hard Boundaries

- Never accept, generate, or inspect plaintexts, passphrases, or derived keys. `Init` issues no key material (no salt).
- Valkey never receives ciphertext, the encrypted filename, the 6-digit code, the sender token, the stream secret, or a raw client hash (`internal/piperelay/stream_test.go:TestStream_ValkeyHoldsNoCiphertextCodeOrToken`). `roomID` is the hex HMAC-SHA-256 of the 6-digit code under `FLICK_RATELIMIT_KEY_SALT`; `Send` writes only `sha256Hex(secret)` of the one-time stream secret into Valkey.
- `Coordinator.serveStream` requires `FLICK_INTERNAL_TOKEN`, a valid room ID, a waiting local `streamSlot`, a streaming room, and the matching stream secret, copying through a pooled, cleared 64 KiB buffer (`copyWithPool`).
- The stream client dials only an IP literal parsed with `netip.ParseAddr` on `FLICK_INTERNAL_HTTP_PORT`, never follows redirects (`http.ErrUseLastResponse`), and never uses an HTTP proxy (`newStreamTransport`).
- Sender calls (`Wait`, `Select`, `Decline`, `Cancel`, `Send`) require the sender token returned by `Init`; a wrong token answers `ErrNotFound`. `Select` and `Decline` answer `ErrNotPending` for a connection ID the session no longer (or never) holds. `Cancel` on an ended session answers `ErrNotFound` (`internal/piperelay/coordinator.go:Coordinator.Cancel`). `Join` and `Select` return `ErrExpired` once the 3-minute window (`internal/piperelay/coordinator.go:defaultTTL`) closed.
- A waiting connection counts as present only while its presence key `{Namespace}poll:{roomID}:{SHA-256 of the connection ID}` exists: `Join` creates it, and each `Status` call refreshes its TTL (`PresenceTimeout`) when it starts and ends (`acquireLeaseScript`, `releaseLeaseScript`). `Coordinator.pruneIdle` drops a waiting connection whose lease expired, freeing the slot without a decline or a burn. `Coordinator.Leave` (`POST /api/pipe/leave`, authorized by the connection ID alone) drops a waiting connection at once on the receiver's own exit, blocks nothing, and answers `ErrNotPending` for a connection already settled, chosen, or gone.
- Residual risk: a holder of the right 6-digit code who keeps polling `Status` keeps its slots, up to `MaxPendingPerNetwork` per network; the sender can `Decline` those rows. A replica killed without `SIGTERM` leaves each of its open `Status` calls counted for up to its hold plus `PresenceTimeout` (`internal/httpapi/piperelay.go:pipeWaitMaxHold` 25 s + `PresenceTimeout` 35 s), so the same connection may get `ErrPollInProgress` until then.
- Never return a connection's `age1pq1…` recipient (`JoinClient.Recipient`, shape-checked by `internal/httpapi/piperelay.go:joinPipe`) to any receiver; only sender-token calls expose it: `Coordinator.Wait` returns `PendingConnection.Recipient` per waiting row and `WaitSnapshot.Recipient` once `WaitSnapshot.ReceiverReady` is true.
- Never log verification numbers, 6-digit codes, room IDs, sender tokens, connection IDs, client keys, recipients, or User-Agents.
- Rendezvous sessions end on transfer completion, sender `Cancel`, the chosen receiver disconnecting before the stream starts (or missing `Receive` within `defaultPresenceTimeout`, 35 s), or the 3-minute `defaultTTL`. Late, extra, declined, idle, room-full (`MaxPendingConnections`, 10), or network-full (`MaxPendingPerNetwork`, 3) receivers never end or burn a session.
- A started stream is never cut by `Cancel` (`ErrStreaming`) or the 3-minute rendezvous `defaultTTL`; only stream failure, either side disconnecting, `MaxTransferDuration` (10 min), or a replica shutdown outlasting `cmd/flick-api/main.go:shutdownTimeout` (50 s) ends it (`TestCoordinator_CancelRacingSendNeverCutsAStream`).
- `Coordinator.Drain` wakes open `Status`/`Wait` calls on `SIGTERM`; `Coordinator.Close` stops local goroutines without deleting rooms from Valkey so other replicas keep serving them.
- `Receive` (before stream arrival) and `waitForReceiver` inside `Send` treat single Valkey read errors as transient and re-read at `resyncInterval` (2 s), because the shared Valkey client (`internal/httpapi/ratelimit.go`) times out after 250 ms without retries.

## Tests

- Coordinator tests require Valkey at `FLICK_TEST_VALKEY_ADDR` and skip when unset. Each test isolates itself with a unique `Namespace`. `podNetwork` in `internal/piperelay/coordinator_test.go` simulates multiple replicas advertising `192.0.2.N` over local listeners via `CoordinatorOptions.StreamTransport`.
