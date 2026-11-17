#!/usr/bin/env bash
set -euo pipefail

failed=0

require_file() {
  local path="$1"

  if [ ! -f "$path" ]; then
    echo "repo-structure: missing $path" >&2
    failed=1
  fi
}

for guide in \
  AGENTS.md \
  .dockerignore \
  .agents/AGENTS.md \
  .agents/skills/pr-review/SKILL.md \
  .github/AGENTS.md \
  .github/ISSUE_TEMPLATE/bug.yml \
  .github/ISSUE_TEMPLATE/config.yml \
  .github/ISSUE_TEMPLATE/task.yml \
  .github/pull_request_template.md \
  .github/workflows/review-gate.yml \
  cmd/AGENTS.md \
  contracts/AGENTS.md \
  deploy/AGENTS.md \
  deploy/base/api.yaml \
  deploy/base/configmap.yaml \
  deploy/base/postgres.yaml \
  deploy/base/valkey.yaml \
  deploy/base/ingress.yaml \
  deploy/base/kustomization.yaml \
  deploy/base/namespace.yaml \
  deploy/base/nats.yaml \
  deploy/base/pvc.yaml \
  deploy/base/secret.yaml \
  deploy/base/web.yaml \
  deploy/base/worker.yaml \
  deploy/k3d/kustomization.yaml \
  docs/AGENTS.md \
  docs/runbook/container-images.md \
  docs/runbook/k3d-smoke.md \
  docs/runbook/k3s-base.md \
  docs/runbook/postgres-cutover.md \
  Dockerfile.api \
  Dockerfile.worker \
  internal/AGENTS.md \
  scripts/ci/k8s-manifests.sh \
  scripts/ci/review-gate.sh \
  scripts/ci/images.sh \
  scripts/ops/migrate-sqlite-to-postgres.sh \
  scripts/AGENTS.md \
  tests/AGENTS.md \
  web/.dockerignore \
  web/AGENTS.md \
  web/Dockerfile \
  web/nginx.conf; do
  require_file "$guide"
done

if [ -f .github/workflows/review-gate.yml ]; then
  # shellcheck disable=SC2016
  if grep -Fq 'ref: ${{ github.event.pull_request.head.sha }}' .github/workflows/review-gate.yml ||
    grep -Fq 'ref: ${{ github.event.pull_request.head.ref }}' .github/workflows/review-gate.yml ||
    grep -Fq 'ref: ${{ github.head_ref }}' .github/workflows/review-gate.yml; then
    echo "repo-structure: review gate must not checkout or execute PR head code" >&2
    failed=1
  fi

  if ! grep -Fq 'persist-credentials: false' .github/workflows/review-gate.yml; then
    echo "repo-structure: review gate checkout must not persist credentials" >&2
    failed=1
  fi
fi

for forbidden in \
  common \
  lib \
  service \
  services \
  shared \
  sprints \
  utils; do
  if [ -e "$forbidden" ]; then
    echo "repo-structure: forbidden top-level path $forbidden" >&2
    failed=1
  fi
done

for forbidden in \
  internal/common \
  internal/lib \
  internal/shared \
  internal/utils \
  deploy/production \
  deploy/overlays/private \
  web/src/lib/utils; do
  if [ -e "$forbidden" ]; then
    echo "repo-structure: forbidden path $forbidden" >&2
    failed=1
  fi
done

if [ -d cmd ]; then
  while IFS= read -r service_dir; do
    require_file "$service_dir/AGENTS.md"

    first_subdir="$(find "$service_dir" -mindepth 1 -maxdepth 1 -type d -print -quit)"
    if [ -n "$first_subdir" ]; then
      echo "repo-structure: service entrypoint must not contain subdirectories: $service_dir" >&2
      failed=1
    fi
  done < <(find cmd -mindepth 1 -maxdepth 1 -type d | sort)
fi

if [ -d internal ]; then
  while IFS= read -r package_dir; do
    require_file "$package_dir/AGENTS.md"
  done < <(find internal -mindepth 1 -maxdepth 1 -type d | sort)
fi

# Valkey must stay memory-only (docs/architecture/security-model.md): no RDB
# snapshot and no append-only file.
if [ -f deploy/base/valkey.yaml ]; then
  if ! grep -Eq -- '- --save$' deploy/base/valkey.yaml ||
    ! grep -Fq -- '- --appendonly' deploy/base/valkey.yaml ||
    ! grep -Fq -- '- "no"' deploy/base/valkey.yaml; then
    echo "repo-structure: deploy/base/valkey.yaml must run valkey-server with --save \"\" --appendonly no" >&2
    failed=1
  fi
fi

# flick-api and flick-worker run more than one replica on any node, so they
# must not mount a ReadWriteOnce data volume again (deploy/base/pvc.yaml).
if grep -Eq 'claimName: flick-(api|worker)-data' deploy/base/*.yaml; then
  echo "repo-structure: flick-api and flick-worker must not mount a data PVC; their state lives in PostgreSQL" >&2
  failed=1
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "repo-structure: ok"
