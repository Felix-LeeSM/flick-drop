#!/usr/bin/env bash
set -euo pipefail

scripts/ci/shell.sh
scripts/ci/repo-structure.sh
scripts/ci/env-contract.sh
scripts/ci/contracts.sh
scripts/ci/go.sh
scripts/ci/web.sh
