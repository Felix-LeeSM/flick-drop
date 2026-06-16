# BurnLink Agent Guide

Read this file first, then read the nearest `AGENTS.md` for the directory you
will change.

## Project Shape

BurnLink is a small self-hosted service with four runtime parts:

- `burnlink-web`: SvelteKit UI.
- `burnlink-api`: HTTP API and owner of `api.db`.
- `burnlink-worker`: NATS JetStream consumer and owner of `worker.db`.
- `nats`: broker for async job delivery.

Go packages under `internal/` are not services. They are library boundaries used
by the commands in `cmd/`.

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
