# Flick Agent Guide

Read this file first, then read the nearest `AGENTS.md` for the directory you
will change.

## Project Shape

Flick is a small self-hosted service with four runtime parts:

- `flick-web`: SvelteKit UI.
- `flick-api`: HTTP API and owner of `api.db`.
- `flick-worker`: NATS JetStream consumer and owner of `worker.db`.
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
- Frontend adapter, Go router, SQLite driver, object storage SDK, container
  base, or ID format change: `docs/architecture/implementation-choices.md`.
- Service, broker, or internal communication change:
  `docs/architecture/service-topology.md`.
- Env var, secret, deploy config, or local setup change:
  `docs/architecture/env-contract.md`.
- CI, test, smoke, or GitHub Actions change:
  `docs/architecture/ci-testing.md`.
- Agent workflow or planning artifact change:
  `docs/architecture/agent-workflow.md`.
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
  files, PVC dumps, and backup archives stay out of the repository.
- Keep public manifests generic. Real production overlays belong outside this
  public repository until a private ops repository exists.

## Development Principles

- Required quality gates live in CI and `scripts/ci/all.sh`, not in mandatory
  local git hooks.
- Keep early implementation explicit and readable. Add shared abstractions only
  when they reduce real duplication or stabilize a real contract.
- Update docs, contracts, and env examples with behavior changes. Do not let
  implementation drift away from the documented service boundaries.
- Every `AGENTS.md` statement must be explicit and self-contained. Name the
  exact file, symbol, env var, command, or value with its path (e.g.
  `internal/config/config.go:144`, `FLICK_TRUSTED_PROXIES`, `RAW_KEY_BYTES`).
  No implied context, no "as mentioned above" or "the above", no bare pronouns
  ("this", "it", "that" referring to another section), and no asserted facts
  without a code or doc reference. A reader landing in any directory must
  understand a statement without reading another file.
- Every interactive element exposes an accessible name. Icon-only buttons and
  links (content is only an icon, e.g. a `<...Icon>` child) carry `aria-label`
  — see `web/src/lib/components/ThemeToggle.svelte`,
  `web/src/lib/components/UrlField.svelte`. Text inputs, selects, and textareas
  use a `<Label for="…">` association or an `aria-label` — see
  `web/src/lib/components/CreateSecretPage.svelte:461`. An element that already
  shows visible text has its name from that text; do not add a redundant
  `aria-label` that duplicates it. Decorative icons paired with visible text get
  `aria-hidden="true"` so they are not announced twice.
