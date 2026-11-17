# `internal/piperelay/` Guide

This package coordinates the live relay ("Live transfer", `/api/pipe/*`):
real-time HTTP pipe transfers between devices that store nothing in the
databases or object storage, served by any number of API replicas.

## Purpose & Scope

- `internal/piperelay/coordinator.go` implements the `Coordinator` type and its
  public calls (`Init`, `Join`, `Status`, `Wait`, `Select`, `Decline`,
  `Cancel`, `ActiveSessions`, `Drain`, `Close`).
- `internal/piperelay/room.go` holds the rendezvous state in Valkey: one hash
  per session, `{Namespace}room:{roomID}` (fields `rev` and `state`, JSON of
  `room`), where `roomID` is the hex HMAC-SHA-256 of the 6-digit code under
  `CoordinatorOptions.KeySalt` (`FLICK_RATELIMIT_KEY_SALT`). Every write is a
  compare-and-set (`casRoomScript`) that publishes on `{Namespace}ev:{roomID}`
  in the same Lua call. `Coordinator.update` re-reads and retries a lost race.
- `internal/piperelay/stream.go` implements `Receive`, `Send`, and the
  pod-to-pod stream listener (`Coordinator.InternalHandler`, mounted by
  `cmd/flick-api/main.go` on `FLICK_INTERNAL_HTTP_PORT`). The pod-to-pod hop
  lives here, not in `internal/httpapi`, because it is the relay's transport,
  not a public API endpoint.
- Each process holds one Pub/Sub pattern subscription
  (`{Namespace}ev:*`) and fans it out to local waiters through `roomHub`
  (`internal/piperelay/room.go`). A waiter registers before it reads the room
  and re-reads at least every `resyncInterval` (2 seconds), so a lost Pub/Sub
  message only delays a wake-up.
- `Coordinator.settle` (`internal/piperelay/room.go`) applies the end rules on
  every read: the 3-minute window closing ends a room with `ErrExpired`; a
  chosen connection that has not opened `Receive` within
  `CoordinatorOptions.PresenceTimeout` (`defaultPresenceTimeout`, 35 seconds)
  ends it with `ErrSessionClosed`; a streaming room ends only past
  `MaxTransferDuration` (`defaultMaxTransferDuration`, 10 minutes).
  `Coordinator.cleanOnce` runs the same rules on every live room every
  `CleanInterval` (30 seconds) on every replica.
- `Coordinator.Join` admits each receiver that entered the code as a
  connection with an unguessable crypto/rand ID. Up to `MaxPendingConnections`
  (10) connections wait, at most `MaxPendingPerNetwork` (3) per
  `ClientNetwork` (the HTTP layer hashes the IPv4 address or IPv6 /64; the next
  one gets `ErrNetworkFull`), each with its own crypto/rand 3-digit
  verification number, until the sender calls `Select` (the others end with
  `ErrNotSelected`) or `Decline` (that connection ends with `ErrDeclined`, its
  client key is blocked for the session, the slot reopens).
- `JoinClient.Recipient` is the receiver's browser-generated age recipient
  (public key; `internal/httpapi/piperelay.go` `joinPipe` validates its
  shape). The coordinator stores it on the connection and returns it only to
  the sender: `PendingConnection.Recipient` per waiting row, and
  `WaitSnapshot.Recipient` for the attached receiver once
  `WaitSnapshot.ReceiverReady` is true. `Init` issues no key material (no
  salt).
- A waiting receiver long-polls `Coordinator.Status` and calls `Receive` only
  after `Status` reports it selected; `Receive` on a still-waiting connection
  returns `ErrAwaitingDecision`. A waiting connection counts as present only
  while its poll lease `{Namespace}poll:{roomID}:{SHA-256 of the connection
  ID}` exists: `Join` creates it, and each `Status` call refreshes its TTL
  (`PresenceTimeout`) when it starts and ends (`acquireLeaseScript`,
  `releaseLeaseScript`). `Coordinator.pruneIdle` drops a waiting connection
  whose lease expired; dropping frees the slot without a decline or a burn.
- One connection holds at most `MaxStatusPollsPerConnection` (2) open
  `Status` calls (the lease value); the next gets `ErrPollInProgress`. The
  HTTP layer also rate-limits `/status` per client network
  (`internal/httpapi/piperelay.go` `pipeStatusRatePerMinute`, 120).
- `Coordinator.Drain` makes open `Status` and `Wait` calls answer at once;
  `cmd/flick-api/main.go` calls it on `SIGTERM` before the 50-second
  `shutdownTimeout`, so waiting browsers re-poll through another replica.
- Bounded concurrency: `pipe:rooms` (`{Namespace}rooms`, a sorted set scored by
  each room's deadline) caps live sessions across replicas at
  `FLICK_PIPE_MAX_CONCURRENT` (default 100) in `createRoomScript`.
- Residual risk: a holder of the right 6-digit code who keeps polling `Status`
  keeps its slots, up to `MaxPendingPerNetwork` per network; the sender can
  `Decline` those rows. A replica killed without its shutdown (no `SIGTERM`)
  leaves its connections' poll counts until the lease TTL (35 seconds), so the
  same connection may get `ErrPollInProgress` until then.
- `Join` and `Select` return `ErrExpired` once the 3-minute window closed.
- Sender calls (`Wait`, `Select`, `Decline`, `Cancel`, `Send`) require the
  sender token returned by `Init`; a wrong token answers `ErrNotFound`.
  `Select` and `Decline` answer `ErrNotPending` for a connection ID the session
  no longer (or never) holds. `Cancel` on an ended session answers
  `ErrNotFound`.
- A Valkey error surfaces as `ErrUnavailable` (`internal/httpapi/piperelay.go`
  `writePipeError` answers `503 service_unavailable`).

## Stream Path (`internal/piperelay/stream.go`)

- `Receive` registers a local `streamSlot`, then writes the connection as
  attached and `CoordinatorOptions.AdvertiseIP` (`FLICK_POD_IP`, 127.0.0.1
  outside Kubernetes) into the room, then waits for the stream.
- `Send` claims the sender side once (`room.SenderClaimed`; a second `Send`
  gets `ErrAlreadyConnected`), waits for the receiver (`waitForReceiver`),
  draws a one-time stream secret, writes only its SHA-256 and the stream start
  into the room, and POSTs the body to
  `http://{room.ReceiverIP}:{CoordinatorOptions.InternalPort}` +
  `StreamPathPrefix` + `roomID` with `InternalTokenHeader` and
  `StreamSecretHeader`. The kind and encrypted filename travel only as headers
  on that request.
- The listener (`Coordinator.serveStream`) requires `FLICK_INTERNAL_TOKEN`, a
  valid room ID, a waiting local slot, a streaming room, and the matching
  secret, then copies the body into an `io.Pipe` read by the receiver's
  response through a pooled, cleared 64 KiB buffer (`copyWithPool`). It
  answers `200` only after the receiver's caller finished reading; anything
  else makes `Send` return `io.ErrClosedPipe` (`502 receiver_disconnected`).

## Hard Boundaries

- Never accept, generate, or inspect plaintexts, passphrases, or derived keys.
- Valkey never receives ciphertext, the encrypted filename, the 6-digit code,
  the sender token, the stream secret, or a raw client hash:
  `internal/piperelay/stream_test.go`
  `TestStream_ValkeyHoldsNoCiphertextCodeOrToken` checks every key mid-stream.
- The stream client dials only an IP literal parsed with `netip.ParseAddr` on
  the configured internal port, never follows redirects
  (`http.ErrUseLastResponse`), and never uses an HTTP proxy
  (`newStreamTransport`).
- Rendezvous sessions end on transfer completion, sender `Cancel`, the chosen
  receiver disconnecting before the stream starts (or not calling `Receive`
  within the presence timeout), or the 3-minute TTL. A late, extra, declined,
  idle, room-full, or network-full receiver never ends or counts against a
  session.
- Nothing ends a stream that already started except the stream itself failing,
  a replica shutdown that outlasts `cmd/flick-api/main.go` `shutdownTimeout`, or
  `MaxTransferDuration`: `Coordinator.settle` skips streaming rooms for the
  rendezvous TTL and `Coordinator.Cancel` returns `ErrStreaming`. `Send` marks
  the stream started with a compare-and-set only if the room has not ended, so
  a race with `Cancel` never cuts a started stream
  (`TestCoordinator_CancelRacingSendNeverCutsAStream` requires both outcomes).
- `Coordinator.Close` leaves rooms in Valkey untouched (another replica keeps
  serving them) and stops every goroutine the coordinator started.
- Never log verification numbers, 6-digit codes, room IDs, sender tokens,
  connection IDs, client keys, recipients, or User-Agents.
- Never return a connection's recipient to a receiver: only the sender-token
  calls (`Coordinator.Wait`) expose recipients.

## Tests

- Coordinator tests need Valkey at `FLICK_TEST_VALKEY_ADDR` and skip without
  it. Each test uses its own `Namespace`. `podNetwork` in
  `internal/piperelay/coordinator_test.go` runs several replicas that advertise
  192.0.2.N and maps their stream dials to local listeners through
  `CoordinatorOptions.StreamTransport`.
