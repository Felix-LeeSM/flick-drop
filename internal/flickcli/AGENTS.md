---
type: Agent Guide
title: internal/flickcli Guide
description: CLI HTTP client, share-link parsing, send/open parity with web, and destructive-open ordering rules.
tags: [agent-guide, go, cli]
---

# `internal/flickcli/` Guide

The `flick` command-line client's behavior: API calls, share links, and the
send/open flows. `cmd/flick` parses flags and calls into here.

## What lives here

- `client.go`: HTTP client for the public contract in `contracts/openapi.yaml`.
- `link.go`: parsing and building share URLs, including the fragment key.
- `requestlink.go`: parsing and building request-link URLs
  (`/r/{id}#submit=…&fp=…`, `/r/{id}/receive#receive=…&key=…`), twins of
  `web/src/lib/state/request-links.ts`.
- `send.go`: encrypt locally, create the secret, take the inline or presigned
  upload path.
- `open.go`: probe metadata, derive the access proof, consume, decrypt, write.
- `request.go`: request-link flows — create (fresh identity plus an optional
  memo identity), submit (fingerprint-pinned recipient encryption, inline or
  presigned, frozen attempt body), and the destructive retrieval open reusing
  `open.go`'s reservation, staging, and rescue.
- `pipe.go`: the `/api/pipe/*` live-transfer client and its two flows —
  `LiveSend` (room, `/wait` long poll, 3-digit pick or `-confirm`, streaming
  encryption to the chosen recipient) and `LiveJoin` (fresh identity per
  attempt, `/status` long poll, decrypt under the same destination rules as
  `open.go`). `LiveSend` cancels the room on every exit that does not
  complete the transfer (`internal/flickcli/pipe.go:LiveSend`), and
  `LiveJoin` reserves its text destination before polling `/status` and
  takes it back unless the stream is decrypted
  (`internal/flickcli/pipe.go:LiveJoin`); `internal/flickcli/pipe.go:pipeLongPoll`
  retries one 5xx or dropped connection like `web/src/lib/crypto/pipe.ts`
  `fetchLongPoll`. `internal/flickcli/pipe.go:pipeRequest` never follows a
  redirect, so the pipe credentials stay on the configured origin.
- `credential.go`: parse, validate, and serialize `FLCR1:` credential payloads
  (twin of `web/src/lib/credentials/serialize.ts`), plus the masked stderr
  summary and the `-reveal` rendering.
- `cassette_test.go` with `testdata/cassettes/*.json`: the `TestCassette*`
  tests replay recorded responses of a real deployment offline. Recording is
  a manual run with `FLICK_CASSETTE_RECORD=<deployment url>` that uses fake
  payloads and consumes every secret it creates, so a committed cassette
  holds only dead tokens.
- `SendOptions.Identity` and `RequestOptions.Identity`/`MemoIdentity`
  (`send.go`, `request.go`) pin delivery keys for that recording only.
  `cmd/flick` never sets them; a caller must not reuse a pinned identity
  across deliveries, because each delivery needs a fresh key
  (`docs/architecture/security-model.md`).

## Boundaries

- This package is an **outside caller**, a peer of `web/src/lib/api`. It speaks
  only the public HTTP contract.
- Never import `internal/secrets`, `internal/db`, `internal/storage`, or
  `internal/httpapi`. If the CLI needs something the browser cannot get, that
  belongs in the API contract, not in a shortcut through server internals.
- All encryption goes through `internal/clientcrypto`. No age, scrypt, or
  PBKDF2 calls here beyond its functions.
- Keep parity with `web/src/lib/api/secrets.ts` on routing decisions: the inline
  versus object-storage threshold, the create/upload/finalize sequence, and the
  Model A/Model B field pairing (`access` and `wrapped_identity` travel
  together or not at all).

## Opening is destructive

`POST /api/secrets/{id}/open` consumes the secret. The order in `open.go` is
load-bearing:

1. fetch metadata (cheap, non-destructive) to learn whether a passphrase is
   needed,
2. prompt for it and check the link carries a key,
3. only then open.

Anything that can fail should fail before step 3, including reserving the
output destination. After it, the payload exists only in this process — a
failure past that point loses the secret for good.

- A text secret is decrypted into memory, so a failed write can still be
  rescued to a file by the caller (`cmd/flick/main.go:writeRescueFile`);
  values never fall back to stdout. Its reported size is checked against the
  inline limit before the
  open, since the server's number is otherwise the only bound.
- A file secret is streamed: age decrypts the body into a `.flick-partial-*`
  staging file in the destination directory, and only after the final chunk
  authenticates is it moved into place (rename onto a reserved `-output`, or a
  hard link to an unused name, or an exclusive-create copy where hard links
  are unavailable). A body that fails to authenticate leaves
  nothing behind. A staging file that cannot be moved into place is reported
  as `OpenResult.RescuedPath`. A disk that fails mid-stream loses the file;
  there is no second copy to fall back on.
- Send encrypts a file into a temporary file of ciphertext and uploads from it,
  so neither direction holds a whole file in memory.

Writing a file secret never overwrites an existing file, and the decrypted
filename is sender-controlled input: reduce it to a base name before joining it
to the output directory.
