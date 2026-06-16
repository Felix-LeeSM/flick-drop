# `internal/secrets/` Guide

This package owns the secret lifecycle domain: create, fetch metadata, consume,
expire, and cleanup decisions.

Directory structure:

- Keep lifecycle flows readable and close together.
- Use named files for create, open, consume, expiry, and cleanup behavior if the
  package grows.
- Do not create generic utility folders.

Rules:

- The domain must assume the server never knows plaintext, passphrases, or
  derived keys.
- Share links expose only secret IDs.
- Deletion semantics must match the documented residual-risk model.
- Storage, events, and database details should enter through explicit
  dependencies, not hidden globals.
