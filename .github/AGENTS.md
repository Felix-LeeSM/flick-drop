# `.github/` Guide

GitHub Actions should call `scripts/ci/*` instead of duplicating check logic.

CI is the required project gate. Do not depend on local git hooks for repository
correctness.

PR checks should stay fast and deterministic:

- shell/static structure checks
- Go unit/integration checks when `go.mod` exists
- web checks when `web/package.json` exists
- contract/env checks
- NATS compose smoke

k3d and real OCI smoke tests should run only on schedule, manual dispatch, or a
dedicated workflow after deploy manifests and credentials exist.

Repository policy:

- `main` is protected.
- Require PRs, `Repo checks`, and `NATS smoke` before merging.
- Do not require `k3d smoke` for PR merge.
- Keep squash merge as the default merge strategy.
