# `internal/piperelay/` Guide

This package coordinates real-time, in-memory HTTP pipe relays for zero-storage
secret transfer between devices.

## Purpose & Scope

- `internal/piperelay/coordinator.go` implements the in-memory `Coordinator` type.
- Manages ephemeral rendezvous sessions pairing a sending client and a receiving
  client via a single-use 6-digit numeric code.
- `Coordinator.Join` in `internal/piperelay/coordinator.go` admits each receiver
  that entered the code as a connection with an unguessable crypto/rand ID.
  With `InitOptions.RequireConfirmation` on, up to `MaxPendingConnections` (10)
  connections wait, each with its own crypto/rand 3-digit verification number,
  until the sender calls `Select` (the others end with `ErrNotSelected`) or
  `Decline` (that connection ends with `ErrDeclined`, its client key is blocked
  for the session, the slot reopens). With confirmation off the first
  connection to call `Receive` wins and later ones get `ErrAlreadyConnected`.
- Sender calls (`Wait`, `Select`, `Decline`, `Cancel`, `Send`) require the
  sender token returned by `Init`; a wrong token answers `ErrNotFound`.
- Ephemeral state is stored exclusively in memory (`map[[32]byte]*Session`) with
  a 3-minute time-to-live (`time.Duration`).
- Bounded concurrency governed by `FLICK_PIPE_MAX_CONCURRENT` (default 100).
- Transfers bytes between sender request bodies and receiver response streams
  using Go `io.Pipe()`, maintaining a bounded 64 KiB buffer (`sync.Pool`).
- Stores zero bytes to SQLite (`internal/db`) or Object Storage (`internal/storage`).

## Hard Boundaries

- Never accept, generate, or inspect plaintexts, passphrases, or derived keys.
- Codes are indexed exclusively by SHA-256 hash (`[32]byte`).
- Rendezvous sessions end on transfer completion, sender `Cancel`, the chosen
  receiver disconnecting before the stream starts, or the 3-minute TTL. A late,
  extra, declined, or room-full receiver never ends or counts against a session.
- Nothing ends a stream that already started except the stream itself failing,
  `Coordinator.Close()`, or `maxTransferDuration` (10 minutes):
  `Coordinator.cleanExpired` skips streaming sessions for the rendezvous TTL and
  `Coordinator.Cancel` returns `ErrStreaming`.
- Never log verification numbers, 6-digit codes, sender tokens, connection IDs,
  client keys, or User-Agents.
- No goroutines or background workers may survive beyond `Coordinator.Close()`.
