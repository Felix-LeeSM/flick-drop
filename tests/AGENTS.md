---
type: Agent Guide
title: tests Guide
description: Cross-component integration, browser e2e, and non-secret fixture boundaries under tests/.
tags: [agent-guide, tests]
---

# `tests/` Guide

Cross-component tests live here when they do not naturally belong inside Go or
SvelteKit package directories.

Expected areas:

- `integration/`: API, worker, PostgreSQL, and NATS integration tests.
- `e2e/`: browser-driven secret create/open/consume flows.
- `fixtures/`: non-secret test payloads and schema fixtures.

Directory structure principles:

- Go package unit tests stay beside the Go package they test.
- Svelte component and browser-unit tests stay under `web/`.
- Put tests here only when they cross component, process, storage, broker, or
  browser boundaries.
- Keep fixtures grouped by contract or scenario, not by test framework.

Test fixtures must not contain real secrets, real OCI identifiers, real bucket
names, passphrases, or derived keys from user data.

Test principles:

- The main browser flow is create, share ID-only link, enter passphrase, decrypt
  once, and verify consumed or expired behavior.
- Integration tests should prove service ownership boundaries: API owns
  the `flick_api` PostgreSQL database (`FLICK_DATABASE_URL`), worker owns the
  `flick_worker` database (`FLICK_WORKER_DATABASE_URL`), and worker mutations
  go through internal API calls.
- OCI tests must be opt-in through environment variables and safe against
  accidental production buckets.
- Prefer small deterministic fixtures. Use dummy ciphertext when encrypted
  payload shape matters.
