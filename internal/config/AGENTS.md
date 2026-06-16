# `internal/config/` Guide

This package owns environment parsing and validation.

Directory structure:

- Keep the package flat unless configuration grows into multiple stable
  subdomains.
- Prefer files grouped by concern, such as app config, storage config, NATS
  config, and OCI config.
- Do not add a generic `helpers` or `utils` file.

Rules:

- The public environment contract lives in `.env.example` and
  `docs/architecture/env-contract.md`.
- Config parsing may validate shape and required values, but it must not open
  databases, connect to NATS, call OCI, or start services.
- Browser-visible values must use the documented `PUBLIC_` boundary.
