# `web/` Guide

The frontend is SvelteKit.

Directory structure principles:

- Keep the SvelteKit app under `web/`; do not create a sibling frontend app.
- Expected structure after scaffold:
  - `src/routes/`: pages and route-level load/actions.
  - `src/lib/api/`: typed API client and response mapping.
  - `src/lib/crypto/`: Web Crypto, KDF, nonce, and encryption helpers.
  - `src/lib/credentials/`: browser-only credential envelope build/parse/serialize (templates, schema).
  - `src/lib/components/`: reusable UI components.
  - `src/lib/state/`: browser-only state that does not persist passphrases or
    derived keys.
  - `static/`: public static assets safe to ship unchanged.
- Do not add a generic `src/lib/utils/` directory. A single `src/lib/utils.ts`
  holding only the shadcn-svelte `cn()` class helper is allowed; any other
  behavior belongs in a named module.
- Browser crypto code should stay isolated enough that e2e tests can exercise the
  product flow without mocking the API contract.

Product principles:

- The first screen should be the usable secret create/open flow, not a marketing
  page.
- Keep the UI focused on short-lived one-time delivery. Avoid account, inbox,
  collaboration, or long-term storage assumptions unless the product scope
  changes explicitly.
- Communicate failure and expiry states plainly without implying guaranteed
  physical erasure.

Security invariants:

- `web/src/lib/crypto/text.ts` derives Model A keys from user-entered
  passphrases; Model B generates a random key in the browser.
- `web/src/lib/api/secrets.ts:createShareUrl` puts only the secret ID in the
  path and puts Model B keys only in the `#key=...` fragment, as specified in
  `docs/architecture/security-model.md`. Never put keys in URL paths/queries.
- Never send passphrases, derived keys, plaintext secret content, or plaintext
  filenames to the API.
- Encrypt text and files with Web Crypto before upload.
- Keep passphrases and encryption keys out of localStorage, sessionStorage,
  IndexedDB, `history.state`, telemetry, and error reports. Permitted key-bearing
  fragments are the Model B recipient link defined by
  `web/src/lib/crypto/fragment.ts` and the requester-only retrieval link
  defined by `docs/architecture/request-links.md`; passphrases never enter URLs.
- `contracts/sender-management-v1.md` defines the M8 management URL
  `/m/{id}#manage={token}` without an encryption key. Pass the recipient URL
  from create to management only in browser memory; a refreshed or new-device
  management visit offers status/cancellation without reconstructing that URL.
- `src/lib/state/created-delivery.ts` holds only one in-memory recipient-link
  handoff, consumed by `src/lib/components/ManageSecretPage.svelte`. Never move
  the handoff into browser storage or `goto()` state. The management route
  `src/routes/m/[id]/+page.svelte` remounts when the ID or fragment changes.
- `contracts/sender-management-v1.md` requires separate recipient-sharing and
  private-management-link actions. Never share the management page location
  as the recipient URL or send a management token to recipient `/open`.
- Treat API responses as ciphertext plus metadata until browser-side decrypt
  succeeds.

Use `PUBLIC_` environment variables only for values safe to ship to the browser.
Do not expose internal tokens, OCI settings, NATS URLs, or server-only config.

Request-link boundary:

- `docs/architecture/request-links.md` defines M9's RSA-OAEP/AES-GCM
  envelope and separate submission/retrieval authority; request crypto belongs
  in `src/lib/crypto/requests.ts`, independently of existing `text.ts` formats.
- `docs/architecture/request-links.md` permits the requester-only PKCS#8 key in
  `/r/{id}/receive#receive=...&key=...` for explicit private-link custody. The
  request key must never enter HTTP, path/query, browser storage, telemetry,
  or `history.state`. A submission link must never contain the requester key.
- `src/lib/components/ReceiveRequestPage.svelte` validates the private key against
  owner metadata and requires verified configuration before consuming Open.
  `src/lib/api/config.ts:getVerifiedConfig` rejects failed or malformed responses
  instead of substituting advisory creation limits.
- `src/lib/api/requests.ts` retains exact encrypted attempt bytes in browser
  memory. `src/lib/components/SubmitRequestPage.svelte` confirms a matching
  acceptance receipt or an abandoned generation before changing attempts.
