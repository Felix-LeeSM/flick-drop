# `internal/clientcrypto/` Guide

Client-side encryption for non-browser clients. This is the Go mirror of
`web/src/lib/crypto/`, and nothing else.

## What lives here

- `crypto.go`: PBKDF2 key derivation, AES-GCM seal/open, the access verifier,
  the encrypted-filename envelope.
- `fragment.go`: the Model B key in a share URL fragment (`#key=...`).
- Golden vector tests against `tests/fixtures/client-crypto-vectors.json`.

## The rule that matters

This package is a **second implementation of a wire format**, not a library the
browser also uses. Every constant here has a twin in
`web/src/lib/crypto/text.ts`:

- `KDFIterations` (600,000) and the server-enforced minimum
- `KeyLengthBits`, `SaltBytes`, `NonceBytes`, `RawKeyBytes`
- `AccessVerifierPurpose` and the `purpose\0passphrase` material
- the `{nonce, ciphertext}` filename envelope
- base64 for the wire, base64url for the fragment

Change one on either side and links created by one client stop opening in the
other, with an AEAD tag mismatch as the only symptom. So:

- **Change both sides in the same commit, or neither.**
- The golden vectors are a contract, not a snapshot. A failing vector means the
  format moved and every link already in flight is affected — do not regenerate
  the fixture to make a test pass.
- Adding a vector is fine. Editing one needs a reason in the PR body.

## Boundaries

- No HTTP, no file I/O, no flag parsing. Callers own those.
- Never import `internal/secrets`, `internal/storage`, or `internal/db`. This is
  client-side code and must not gain access the browser does not have.
- KDF parameters arriving from the server are untrusted input: validate with
  `ValidateKDF` before deriving. A lowered iteration count is a downgrade
  attempt.
- `crypto/cipher` panics on a wrong-length nonce. Anything decoded off the wire
  is length-checked before it reaches AES-GCM.
