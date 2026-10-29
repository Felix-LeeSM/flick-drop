# `internal/clientcrypto/` Guide

Client-side encryption for non-browser clients. This is the Go mirror of
`web/src/lib/crypto/`, and nothing else.

## What lives here

- `age.go`: the secret format (`contracts/age-format-v1.md`) — hybrid
  identity, passphrase-wrapped identity, streaming encrypt/decrypt. Interop
  files in `tests/fixtures/age/` are opened by both this package and
  `web/src/lib/crypto/age.test.ts`.
- `access.go`: the Model A PBKDF2 access proof, checked against the
  `access_proof` vector in `tests/fixtures/client-crypto-vectors.json`.
- `fragment.go`: the Model B identity in a share URL fragment (`#key=...`).

## The rule that matters

This package is a **second client of a wire format**, not a library the
browser also uses. Its twins in `web/src/lib/crypto/` are `age.ts`,
`access.ts`, and `fragment.ts`:

- age itself comes from `filippo.io/age`; Flick adds no cipher, KDF, or chunk
  layout. Never hand-roll one here.
- `WrapWorkFactor` (18) and `MaxWrapWorkFactor` (20) match the browser.
- `KDFIterations` (600,000) and the server-enforced minimum,
  `KeyLengthBits`, `SaltBytes`, `AccessVerifierPurpose`, and the
  `purpose\0passphrase` material
- base64 for the wire, the uppercase identity string for the fragment

Change one on either side and links created by one client stop opening in the
other. So:

- **Change both sides in the same commit, or neither.**
- The vectors and interop files are a contract, not a snapshot. A failing one
  means the format moved and every link already in flight is affected — do not
  regenerate a fixture to make a test pass.
- Adding a vector is fine. Editing one needs a reason in the PR body.

## Boundaries

- No HTTP, no file I/O, no flag parsing. Callers own those.
- Never import `internal/secrets`, `internal/storage`, or `internal/db`. This is
  client-side code and must not gain access the browser does not have.
- KDF parameters arriving from the server are untrusted input: validate with
  `ValidateKDF` before deriving. A lowered iteration count is a downgrade
  attempt.
- A wrapped identity arrives from the server: `UnwrapIdentity` caps the scrypt
  work factor and the unwrapped size. Keep both caps.
