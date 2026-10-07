# `internal/flickcli/` Guide

The `flick` command-line client's behavior: API calls, share links, and the
send/open flows. `cmd/flick` parses flags and calls into here.

## What lives here

- `client.go`: HTTP client for the public contract in `contracts/openapi.yaml`.
- `link.go`: parsing and building share URLs, including the fragment key.
- `send.go`: encrypt locally, create the secret, take the inline or presigned
  upload path.
- `open.go`: probe metadata, derive the access proof, consume, decrypt, write.

## Boundaries

- This package is an **outside caller**, a peer of `web/src/lib/api`. It speaks
  only the public HTTP contract.
- Never import `internal/secrets`, `internal/db`, `internal/storage`, or
  `internal/httpapi`. If the CLI needs something the browser cannot get, that
  belongs in the API contract, not in a shortcut through server internals.
- All encryption goes through `internal/clientcrypto`. No AES or PBKDF2 calls
  here.
- Keep parity with `web/src/lib/api/secrets.ts` on routing decisions: the inline
  versus object-storage threshold, the create/upload/finalize sequence, and the
  Model A/Model B field pairing (`kdf` and `access` travel together or not at
  all).

## Opening is destructive

`POST /api/secrets/{id}/open` consumes the secret. The order in `open.go` is
load-bearing:

1. fetch metadata (cheap, non-destructive) to learn whether a passphrase is
   needed,
2. prompt for it and check the link carries a key,
3. only then open.

Anything that can fail should fail before step 3. After it, the payload exists
only in this process — a failure past that point loses the secret for good,
which is why `Open` decrypts into memory and reports the written path rather
than streaming into a file handle that might not open.

Writing a file secret never overwrites an existing file, and the decrypted
filename is sender-controlled input: reduce it to a base name before joining it
to the output directory.
