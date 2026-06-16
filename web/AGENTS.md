# `web/` Guide

The frontend is SvelteKit.

Directory structure principles:

- Keep the SvelteKit app under `web/`; do not create a sibling frontend app.
- Expected structure after scaffold:
  - `src/routes/`: pages and route-level load/actions.
  - `src/lib/api/`: typed API client and response mapping.
  - `src/lib/crypto/`: Web Crypto, KDF, nonce, and encryption helpers.
  - `src/lib/components/`: reusable UI components.
  - `src/lib/state/`: browser-only state that does not persist passphrases or
    derived keys.
  - `static/`: public static assets safe to ship unchanged.
- Do not add a generic `src/lib/utils/`. Create a named module for the behavior
  being owned.
- Browser crypto code should stay isolated enough that e2e tests can exercise
  the product flow without mocking the API contract.

Product principles:

- The first screen should be the usable secret create/open flow, not a marketing
  page.
- Keep the UI focused on short-lived one-time delivery. Avoid account, inbox,
  collaboration, or long-term storage assumptions unless the product scope
  changes explicitly.
- Communicate failure and expiry states plainly without implying guaranteed
  physical erasure.

Security invariants:

- Derive encryption keys in the browser from user-entered passphrases.
- Share links contain only secret IDs.
- Never send passphrases, derived keys, plaintext secret content, or plaintext
  filenames to the API.
- Encrypt text and files with Web Crypto before upload.
- Keep passphrases and derived keys out of localStorage, sessionStorage,
  IndexedDB, URLs, telemetry, and error reports.
- Treat API responses as ciphertext plus metadata until browser-side decrypt
  succeeds.

Use `PUBLIC_` environment variables only for values safe to ship to the browser.
Do not expose internal tokens, OCI settings, NATS URLs, or server-only config.
