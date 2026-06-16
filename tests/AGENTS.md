# `tests/` Guide

Cross-component tests live here when they do not naturally belong inside Go or
SvelteKit package directories.

Expected areas:

- `integration/`: API, worker, SQLite, and NATS integration tests.
- `e2e/`: browser-driven secret create/open/consume flows.
- `fixtures/`: non-secret test payloads and schema fixtures.

Test fixtures must not contain real secrets, real OCI identifiers, real bucket
names, or decrypt keys from user data.
