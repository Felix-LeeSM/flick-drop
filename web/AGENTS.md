---
type: Agent Guide
title: web Guide
description: Directory structure, security invariants, request-link rules, design/prototyping gotchas, and i18n testing policy for the SvelteKit frontend.
tags: [agent-guide, web, sveltekit, security]
---

# `web/` Guide

The frontend is SvelteKit.

Directory structure principles:

- Keep the SvelteKit app under `web/`; do not create a sibling frontend app.
- Expected structure:
  - `src/routes/`: pages and route-level load/actions.
  - `src/lib/api/`: typed API client and response mapping.
  - `src/lib/crypto/`: age (`age-encryption`) sealing for secrets and request links, and the PBKDF2 access proof. `src/lib/crypto/pipe.ts` holds the live relay (send-by-code) client: `/api/pipe/*` calls, the receiver's age key (`createPipeReceiverKey`), and filename encryption.
  - `src/lib/credentials/`: browser-only credential envelope build/parse/serialize (templates, schema).
  - `src/lib/components/`: reusable UI components.
  - `src/lib/state/`: browser-only state that does not persist passphrases or derived keys.
  - `src/lib/fonts/`: generated font subsets; `src/lib/fonts/title-kr/subset.py` rebuilds them.
  - `static/`: public static assets safe to ship unchanged.
- Do not add a generic `src/lib/utils/` directory. A single `src/lib/utils.ts` holding only the shadcn-svelte `cn()` class helper is allowed; any other behavior belongs in a named module.
- Browser crypto code should stay isolated enough that e2e tests can exercise the product flow without mocking the API contract.

Product and design principles:

- The first screen is the usable secret create/open flow, not a marketing page. Keep the UI focused on short-lived one-time delivery without account, inbox, collaboration, or long-term storage assumptions.
- Communicate failure and expiry states plainly without implying guaranteed physical erasure.
- Project workflow: the main agent drafts visual and UI/UX design (layout, tokens, interaction, mockups) itself; subagents may review architecture or logic but do not produce the visual design.
- Project workflow: keep the current teal-and-serif design system and improve within it; full redesigns were rejected on 2026-11-16 (`docs/log.md`). When the user picks a UI candidate, implement it and show it as a prototype without re-asking about conflicts with older UI rules or tests; update conflicting tests and name them in the report. Stop and ask only for security or data-loss issues.
- UI wording uses user-perspective terms, never calls a passphrase "additional" (`web/src/lib/i18n/README.md`), and never adds "(optional)" to a field a toggle reveals.

Styling and prototyping gotchas:

- `web/src/routes/layout.css` uses `@theme inline`, which does not emit `--font-serif` or `--font-sans` custom properties; reference font families directly in custom CSS. When mixing an achromatic token (hue 0, such as `--card: oklch(1 0 0)`) or tokens with distant hues, use `color-mix(in srgb, …)`: `in oklch` takes the short hue arc and drifts pink or purple. Near-hue `oklch` mixes are fine (`layout.css` `body` glow).
- Use `rem` font sizes (`web/src/app.html` sets `<meta name="text-scale" content="scale">`), keep placeholder and typed input text the same size, and pair `text-[…]` with its line-height in one class (`text-[0.8125rem]/[1]`) because `cn()` (`web/src/lib/utils.ts`, `tailwind-merge`) drops an earlier `leading-none` when a `text-[…]` size follows. `Button` `size="cta"` (`web/src/lib/components/ui/button/button.svelte`) includes `w-full`; add `w-auto` for inline toolbar buttons.
- Tailwind v4 source detection (`@tailwindcss/oxide` 4.3.1) skips paths in `.git/info/exclude`, so classes used only in a git-excluded prototype folder produce no CSS. `docs/runbook/local-dev.md` ("Prototype Folders") has the `@source` fix plus prototype index, focus-outline, and card-shadow tips. Scoped `<style>` class names must not collide with global utilities (`.ring`, `.outline`, `.border`, `.shadow`). Restart `vite dev` after adding classes in a brand-new `.svelte` file.

i18n testing:

- Test translated UI copy by catalog key (for example `ko.create.titleLink`) with one non-English locale rather than repeating assertions across every language. `web/src/lib/i18n/catalogs.test.ts` is the only place every language is checked: each catalog must have every `en` key with the same shape and no empty string. Keep one catalog object per language; do not introduce per-screen i18n scopes.

Security invariants:

- `contracts/age-format-v1.md` defines the secret format. Every secret gets a fresh age identity in the browser (`web/src/lib/crypto/age.ts`). Model A wraps it with the user-entered passphrase (age scrypt in `web/src/lib/crypto/scrypt.worker.ts`) and sends a separate PBKDF2 access proof (`web/src/lib/crypto/access.ts`); Model B carries it in `#key=...`. Use the library; never hand-roll encryption or chunking.
- `web/src/lib/api/secrets.ts:createShareUrl` puts only the secret ID in the path and puts Model B identities only in `#key=...` (`docs/architecture/security-model.md`). Never put keys in URL paths/queries, and never send passphrases, derived keys, plaintext content, or plaintext filenames to the API.
- Encrypt text and files with age before upload (`web/src/lib/crypto/secret.ts`). Show decrypted content only after age authenticates the final chunk; never offer a partial download.
- Keep passphrases and encryption keys out of `localStorage`, `sessionStorage`, IndexedDB, `history.state`, telemetry, and error reports. Permitted key-bearing fragments are Model B (`web/src/lib/crypto/fragment.ts`) and requester retrieval (`docs/architecture/request-links.md`); passphrases never enter URLs.
- `contracts/sender-management-v1.md` defines `/m/{id}#manage={token}` without an encryption key. Pass the recipient URL from create to management only in browser memory: `src/lib/state/created-delivery.ts` holds one in-memory handoff consumed by `src/lib/components/ManageSecretPage.svelte`; never move it to browser storage or `goto()` state. A refreshed or new-device management visit offers status/cancellation without reconstructing that URL. `src/routes/m/[id]/+page.svelte` remounts when the ID or fragment changes.
- Until the recipient link is handed off (a successful `UrlField` `oncopied`, a successful `NativeShareButton` `onshared`, opening the QR code, or a manual copy event), `ManageSecretPage.svelte` warns before leaving through SvelteKit `beforeNavigate`, as `contracts/sender-management-v1.md` requires; keep that warning.
- `contracts/sender-management-v1.md` requires separate recipient-sharing and private-management-link actions. Never share the management page location as the recipient URL or send a management token to recipient `/open`.
- The live relay receiver's age identity from `src/lib/crypto/pipe.ts:createPipeReceiverKey` stays in that receive attempt's memory only (`src/lib/components/ReceiveCodeModal.svelte:joinAndReceive`). Never put it in browser storage, URLs, requests, logs, or telemetry; `POST /api/pipe/join` sends only its `age1pq1…` recipient.
- Treat API responses as ciphertext plus metadata until browser-side decrypt succeeds.
- Use `PUBLIC_` env vars only for values safe to ship to the browser. Do not expose internal tokens, OCI settings, NATS URLs, or server-only config.

Request-link boundary:

- `docs/architecture/request-links.md` defines request links on age (`src/lib/crypto/requests.ts`, `src/lib/crypto/age.ts`). The requester-only `AGE-SECRET-KEY-PQ-1…` identity is permitted only in `/r/{id}/receive#receive=...&key=...`, never in HTTP, path/query, browser storage, telemetry, `history.state`, or submission links.
- `src/lib/components/ReceiveRequestPage.svelte` validates the identity against owner metadata and requires verified configuration before consuming Open. `src/lib/api/config.ts:getVerifiedConfig` rejects failed or malformed responses instead of substituting advisory creation limits.
- `src/lib/api/requests.ts` retains exact encrypted attempt bytes in browser memory; `src/lib/components/SubmitRequestPage.svelte` confirms a matching acceptance receipt or abandoned generation before changing attempts.
