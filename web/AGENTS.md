# `web/` Guide

The frontend is SvelteKit.

Security invariants:

- Derive encryption keys in the browser from user-entered passphrases.
- Share links contain only secret IDs.
- Never send passphrases, derived keys, plaintext secret content, or plaintext
  filenames to the API.
- Encrypt text and files with Web Crypto before upload.
- Treat API responses as ciphertext plus metadata until browser-side decrypt
  succeeds.

Use `PUBLIC_` environment variables only for values safe to ship to the browser.
Do not expose internal tokens, OCI settings, NATS URLs, or server-only config.
