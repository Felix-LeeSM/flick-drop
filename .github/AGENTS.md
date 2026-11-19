---
type: Agent Guide
title: .github Guide
description: GitHub Actions workflow boundaries, branch protection, synthetic canary budget, and metadata-only review gate policy.
tags: [agent-guide, github, ci]
---

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

`.github/workflows/canary.yml` is the scheduled synthetic canary: every 15
minutes it runs `scripts/smoke/canary.sh` (`health`, `text`, `file`) against
the origin in the repository variable `FLICK_CANARY_URL`, and skips when that
variable is unset. It creates two secrets per run (at most four with its single
65-second retry) to stay under
`internal/config/defaults.go:defaultCreateRatePerMinute`; keep any new check
within that budget. Details: `docs/architecture/ci-testing.md`, "Synthetic
Canary".

Repository policy:

- `main` is protected.
- Require PRs, `Repo checks`, `NATS smoke`, and `Review gate` before merging
  after the review gate workflow is installed on `main`.
- Do not require `k3d smoke` for PR merge.
- Keep squash merge as the default merge strategy.

Review gate policy:

- Run the gate on `pull_request` events for metadata checks only. (2026-11-16:
  switched from `pull_request_target` after that pipeline began failing at
  startup for this repository following a GitHub incident.) The checkout stays
  pinned to the base commit, so PR head code is still never executed.
- Do not checkout, build, test, or execute PR head code in review-gate jobs.
- Keep `permissions` least-privilege.
- Require a `## Flick Subagent Review` PR comment created after the latest
  commit and carrying the exact PR head SHA.
- Publish the required `Review gate` commit status against the PR head SHA.
- `review:approved` must be applied after the latest approving subagent review
  comment.
- `review:changes-requested` must block merge.
