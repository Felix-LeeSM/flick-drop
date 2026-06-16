# `scripts/` Guide

Scripts are the shared local and CI entrypoints.

- `scripts/ci/*`: deterministic checks used by GitHub Actions and `mise run`.
- `scripts/smoke/*`: scenario checks against running services.

Scripts should be safe to run before the app is fully scaffolded. If a component
does not exist yet, print a clear skip message and exit successfully.

`scripts/ci/repo-structure.sh` owns static enforcement for directory rules that
can be checked without running the application.

Do not embed credentials, production URLs, derived keys, or plaintext secret
samples. Smoke tests may use dummy ciphertext only.

Automation principles:

- CI scripts are the required quality gate. Local git hooks, if introduced
  later, must be opt-in wrappers around the same commands.
- Prefer deterministic checks that work in a fresh clone with only documented
  tools installed.
- Keep smoke tests explicit about skipped prerequisites such as Docker, k3d, OCI
  credentials, or uninitialized Go/Web projects.
