# `.github/` Guide

GitHub Actions should call `scripts/ci/*` instead of duplicating check logic.

PR checks should stay fast and deterministic:

- shell/static structure checks
- Go unit/integration checks when `go.mod` exists
- web checks when `web/package.json` exists
- contract/env checks
- NATS compose smoke

k3d and real OCI smoke tests should run only on schedule, manual dispatch, or a
dedicated workflow after deploy manifests and credentials exist.
