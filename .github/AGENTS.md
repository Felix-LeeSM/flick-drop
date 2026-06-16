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
- Require PRs, `Repo checks`, `NATS smoke`, and `Review gate` before merging
  after the review gate workflow is installed on `main`.
- Do not require `k3d smoke` for PR merge.
- Keep squash merge as the default merge strategy.

Review gate policy:

- Use `pull_request_target` only for metadata checks.
- Do not checkout, build, test, or execute PR head code in review-gate jobs.
- Keep `permissions` least-privilege.
- Require a `## BurnLink Subagent Review` PR comment created after the latest
  commit.
- `review:approved` must be applied after the latest commit.
- `review:changes-requested` must block merge.
