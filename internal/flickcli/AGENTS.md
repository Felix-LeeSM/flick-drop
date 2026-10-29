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

- A text secret (bounded by the inline limit) is decrypted into memory, so a
  failed write can still hand it to stdout.
- A file secret is streamed: age decrypts the body into a `.flick-partial-*`
  staging file in the destination directory, and only after the final chunk
  authenticates is it moved into place (rename onto a reserved `-output`, or a
  hard link to an unused name). A body that fails to authenticate leaves
  nothing behind. A staging file that cannot be moved into place is reported
  as `OpenResult.RescuedPath`. A disk that fails mid-stream loses the file;
  there is no second copy to fall back on.
- Send encrypts a file into a temporary file of ciphertext and uploads from it,
  so neither direction holds a whole file in memory.

Writing a file secret never overwrites an existing file, and the decrypted
filename is sender-controlled input: reduce it to a base name before joining it
to the output directory.
