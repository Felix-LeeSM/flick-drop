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
  connections wait, at most `MaxPendingPerNetwork` (3) per `ClientNetwork`
  (the HTTP layer hashes the IPv4 address or IPv6 /64; the next one gets
  `ErrNetworkFull`), each with its own crypto/rand 3-digit verification number,
  until the sender calls `Select` (the others end with `ErrNotSelected`) or
  `Decline` (that connection ends with `ErrDeclined`, its client key is blocked
  for the session, the slot reopens). With confirmation off the first
  connection to call `Receive` wins and later ones get `ErrAlreadyConnected`.
- A waiting receiver long-polls `Coordinator.Status` and calls `Receive` only
  after `Status` reports it selected; `Receive` on a still-waiting connection
  returns `ErrAwaitingDecision`. A waiting connection counts as present only
  while it polls: `Session.pruneLocked` drops one with no open `Status` call
  for `CoordinatorOptions.PresenceTimeout` (`defaultPresenceTimeout`, 35
  seconds). Dropping frees the slot without a decline or a burn. A selected
  connection that does not call `Receive` within the same timeout ends the
  session, like the chosen receiver leaving: a waiting `Send` enforces that
  deadline itself (`Coordinator.waitForReceiver`), and `Coordinator.cleanExpired`
  catches sessions with no `Send` waiting.
- One connection holds at most `MaxStatusPollsPerConnection` (2) open `Status`
  calls; the next gets `ErrPollInProgress`. The HTTP layer also rate-limits
  `/status` per client network (`internal/httpapi/piperelay.go`
  `pipeStatusRatePerMinute`, 120).
- Residual risk: a holder of the right 6-digit code who keeps polling `Status`
  keeps its slots, up to `MaxPendingPerNetwork` per network; the sender can
  `Decline` those rows.
- `Join` and `Select` return `ErrExpired` once the 3-minute window closed,
  even before `Coordinator.cleanExpired` removes the session.
- Sender calls (`Wait`, `Select`, `Decline`, `Cancel`, `Send`) require the
  sender token returned by `Init`; a wrong token answers `ErrNotFound`.
  `Select` and `Decline` answer `ErrNotPending` for a connection ID the session
  no longer (or never) holds.
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
  receiver disconnecting before the stream starts (or not calling `Receive`
  within the presence timeout), or the 3-minute TTL. A late, extra, declined,
  idle, room-full, or network-full receiver never ends or counts against a
  session.
- Nothing ends a stream that already started except the stream itself failing,
  `Coordinator.Close()`, or `maxTransferDuration` (10 minutes):
  `Coordinator.cleanExpired` skips streaming sessions for the rendezvous TTL and
  `Coordinator.Cancel` returns `ErrStreaming`. `Coordinator.Send` sets
  `Session.streaming` under `Session.mu` only if the session has not ended, and
  `Cancel` and `cleanExpired` check `streaming` and call `Session.endLocked`
  under the same lock hold, so a race between them never cuts a started stream.
- Never log verification numbers, 6-digit codes, sender tokens, connection IDs,
  client keys, or User-Agents.
- No goroutines or background workers may survive beyond `Coordinator.Close()`.
