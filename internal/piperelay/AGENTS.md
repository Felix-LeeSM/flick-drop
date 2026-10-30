# `internal/piperelay/` Guide

This package coordinates real-time, in-memory HTTP pipe relays for zero-storage
secret transfer between devices.

## Purpose & Scope

- `internal/piperelay/coordinator.go` implements the in-memory `Coordinator` type.
- Manages ephemeral rendezvous sessions pairing a sending client and a receiving
  client via a single-use 6-digit numeric code.
- Ephemeral state is stored exclusively in memory (`map[[32]byte]*Session`) with
  a 3-minute time-to-live (`time.Duration`).
- Bounded concurrency governed by `FLICK_PIPE_MAX_CONCURRENT` (default 100).
- Transfers bytes between sender request bodies and receiver response streams
  using Go `io.Pipe()`, maintaining a bounded 64 KiB buffer (`sync.Pool`).
- Stores zero bytes to SQLite (`internal/db`) or Object Storage (`internal/storage`).

## Hard Boundaries

- Never accept, generate, or inspect plaintexts, passphrases, or derived keys.
- Codes are indexed exclusively by SHA-256 hash (`[32]byte`).
- Rendezvous sessions burn immediately upon transfer completion, client disconnect,
  or after 3 failed code guess attempts.
- No goroutines or background workers may survive beyond `Coordinator.Close()`.
