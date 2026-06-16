# `scripts/` Guide

Scripts are the shared local and CI entrypoints.

- `scripts/ci/*`: deterministic checks used by GitHub Actions and `mise run`.
- `scripts/smoke/*`: scenario checks against running services.

Scripts should be safe to run before the app is fully scaffolded. If a component
does not exist yet, print a clear skip message and exit successfully.

Do not embed credentials, production URLs, decrypt keys, or plaintext secret
samples. Smoke tests may use dummy ciphertext only.
