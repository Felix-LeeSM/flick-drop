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
  .agents/AGENTS.md \
  .github/AGENTS.md \
  cmd/AGENTS.md \
  contracts/AGENTS.md \
  deploy/AGENTS.md \
  docs/AGENTS.md \
  internal/AGENTS.md \
  scripts/AGENTS.md \
  tests/AGENTS.md \
  web/AGENTS.md; do
  require_file "$guide"
done

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

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "repo-structure: ok"
