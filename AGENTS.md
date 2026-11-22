---
type: Agent Guide
title: Flick Agent Guide
description: Root guide for Flick project shape, directory principles, required reading, hard security rules, and development standards.
tags: [agent-guide, root]
---

# Flick Agent Guide

Read this file first, then read the nearest `AGENTS.md` for the directory you
will change.

## Project Shape

Flick is a small self-hosted service with four runtime parts:

- `flick-web`: SvelteKit UI.
- `flick-api`: HTTP API and owner of the `flick_api` PostgreSQL database
  (`FLICK_DATABASE_URL`).
- `flick-worker`: NATS JetStream consumer and owner of the `flick_worker`
  PostgreSQL database (`FLICK_WORKER_DATABASE_URL`).
- `nats`: broker for async job delivery.

Go packages under `internal/` are not services. They are library boundaries used
by the commands in `cmd/`.

## Directory Structure Principles

- Directories are split by ownership and reason to change, not by broad
  technical labels.
- Runtime service entrypoints live in `cmd/flick-api`,
  `cmd/flick-worker`, and `web`. Do not introduce a top-level `services/`
  tree unless the architecture changes explicitly.
- Shared Go implementation belongs under named `internal/*` packages. Avoid
  vague `common/`, `utils/`, `shared/`, or `lib/` directories.
- Public contracts belong in `contracts/`. Do not hide service-to-service
  payload shape inside implementation-only packages.
- Component-local tests live beside the component. Cross-process integration and
  browser e2e tests live in `tests/`.
- A new persistent directory should either have a clear owner from this guide or
  add a local `AGENTS.md` explaining its boundary.

## Product Principles

- Flick is an ephemeral delivery service, not a password manager or long-term
  vault.
- The core workflow is create, share, open once, cleanup.
- Prefer a smaller product surface that preserves one-time and short-lived
  behavior over broad sharing or collaboration features.
- Public-facing docs and code should read like a production-facing open-source
  self-hosted product.

## Required Reading

- Storage or data lifecycle change: `docs/architecture/storage-model.md`.
- Security, encryption, token, log, or sensitive data boundary change:
  `docs/architecture/security-model.md`.
- Database schema or migration change:
  `docs/architecture/database-schema.md`.
- NATS message, outbox, worker retry, or event contract change:
  `docs/architecture/event-contract.md`.
- Kubernetes, image, resource budget, or OCI deployment change:
  `docs/architecture/deployment-target.md`.
- Frontend adapter, Go router, database driver, object storage SDK, container
  base, or ID format change: `docs/architecture/implementation-choices.md`.
- Service, broker, or internal communication change:
  `docs/architecture/service-topology.md`.
- Env var, secret, deploy config, or local setup change:
  `docs/architecture/env-contract.md`.
- Live transfer (`/api/pipe/*`) or Valkey rendezvous change:
  `docs/architecture/live-relay.md`.
- Logging, Prometheus metrics, or OpenTelemetry tracing change:
  `docs/architecture/telemetry.md`.
- CI, test, smoke, or GitHub Actions change:
  `docs/architecture/ci-testing.md`.
- Agent workflow, OKF format, or planning artifact change:
  `docs/architecture/agent-workflow.md` (browse all governed docs from the repository-root `index.md`; its `docs/index.md` entry lists `docs/architecture/index.md` and `docs/runbook/index.md`).
- Scope or milestone change: `docs/ROADMAP.md`.

If `docs/work/active/*.md` exists and matches the current task, read it before
editing. Create an active work note only for work that needs a side-channel
contract, open decision log, or multi-step verification record.

## Hard Rules

- Never log plaintext secret content, passphrases, or derived keys.
- Never send passphrases or derived keys to the API, worker, NATS, OCI, logs, or
  metrics.
- NATS messages contain IDs and small metadata only, never ciphertext bodies.
- Real OCI credentials, kubeconfig, admin tokens, production domains, database
  files, PVC dumps, and backup archives stay out of the repository. The one
  named exception is the public demo instance in `README.md`, `README.ko.md`,
  and `README.ja.md`, which is deliberately advertised.
- Keep public manifests generic. Real production overlays belong outside this
  public repository until a private ops repository exists.

## Development Principles

- Required quality gates live in CI and `scripts/ci/all.sh`, not in mandatory
  local git hooks.
- Keep source comments minimal. Write a comment only for what the code cannot
  show (a security invariant, the reason for a bound, an external-system
  workaround, an ordering requirement, a cross-file twin) and keep it to one
  line. Skip godoc or JSDoc that restates a name,
  step narration, section dividers, and history notes.
- Keep early implementation explicit and readable. Add shared abstractions only
  when they reduce real duplication or stabilize a real contract.
- Update docs, contracts, and env examples with behavior changes. Do not let
  implementation drift away from the documented service boundaries.
- Every `AGENTS.md` statement must be explicit and self-contained. Name the
  exact file, symbol, env var, command, or value with its path (e.g.
  `internal/config/config.go:Load`, `FLICK_TRUSTED_PROXIES`,
  `web/src/lib/crypto/access.ts:KDF_ITERATIONS`).
  No implied context, no "as mentioned above" or "the above", no bare pronouns
  ("this", "it", "that" referring to another section), and no asserted facts
  without a code or doc reference. A reader landing in any directory must
  understand a statement without reading another file.
- Every interactive element exposes an accessible name. Icon-only buttons and
  links (content is only an icon, e.g. a `<...Icon>` child) carry `aria-label`
  — see `web/src/lib/components/ThemeToggle.svelte`,
  `web/src/lib/components/UrlField.svelte`. Text inputs, selects, and textareas
  use a `<Label for="…">` association or an `aria-label` — see
  `<Label for="secret-text">` in
  `web/src/lib/components/CreateSecretPage.svelte`. An element that already
  shows visible text has its name from that text; do not add a redundant
  `aria-label` that duplicates it. Decorative icons paired with visible text get
  `aria-hidden="true"` so they are not announced twice.
- Text a user controls (file names, decrypted text, credential title, label,
  value, and notes in `web/src/lib/components/CredentialView.svelte`, typed
  input values) never widens the page or pushes a control off-screen, at 360px
  wide or wider. Wrap long unbroken text with `[overflow-wrap:anywhere]` or
  `break-all`; Tailwind `break-words` (`overflow-wrap: break-word`) does not
  shrink a grid or flex item's minimum width, so a 200-character word still
  widens the page. Truncate single-line values with `w-full truncate` and put
  the full value in `title` — see the `<input>` in
  `web/src/lib/components/UrlField.svelte`; a truncated flex child also needs
  `min-w-0`. Page-level grids set `grid-cols-[minmax(0,1fr)]` so one wide child
  cannot widen the page — see `web/src/lib/components/DeliveryFrame.svelte` and
  `web/src/lib/components/OpenSecretPage.svelte`.
  A component that renders user text gets a browser check that fills it with a
  200-character unbroken value and asserts
  `document.documentElement.scrollWidth <= innerWidth` — see the
  "long unbroken credential text" test in
  `web/src/lib/components/OpenSecretPage.browser.mjs`.
- Layouts hold at 150% text size at 320px wide or wider. The 320px floor
  applies to enlarged text; the rule for 200-character user-controlled text
  keeps its own 360px floor.
  `web/src/app.html` sets `<meta name="text-scale" content="scale">`, so rem
  sizes follow the OS text-size setting; browser checks simulate it with
  `document.documentElement.style.fontSize = '150%'`. A block sized in rem
  inside a narrow row also gets `min-w-0` or `max-w-full` so it can shrink, and
  a square block uses `aspect-square w-*` in place of `size-*` so its height
  shrinks with its width — see `<QrCode>` in
  `web/src/lib/components/QrModal.svelte` and the `lg` caps in
  `web/src/lib/components/VerifyDigits.svelte`. A button whose label can
  outgrow it uses `whitespace-normal` with a `min-h-*` in place of a fixed
  `h-*` — see the `cta` size in `web/src/lib/components/ui/button/button.svelte`.
  A dialog sets `grid-cols-[minmax(0,1fr)]`, `max-h-[calc(100dvh-1.5rem)]`, and
  `overflow-y-auto` — see `web/src/lib/components/ConfirmDialog.svelte`.
  `document.documentElement.scrollWidth` does not see a `position: fixed`
  dialog, so dialog browser checks call `assertDialogFits` from
  `web/src/lib/components/dialog-fit.mjs`.
