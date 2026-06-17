#!/usr/bin/env bash
set -euo pipefail

if [ ! -f web/package.json ]; then
  echo "web: web/package.json not initialized yet; skipped"
  exit 0
fi

cd web

if [ -f pnpm-lock.yaml ]; then
  pnpm install --frozen-lockfile
else
  pnpm install
fi

run_if_present() {
  local name="$1"
  if node -e "const p=require('./package.json'); process.exit(p.scripts && p.scripts[process.argv[1]] ? 0 : 1)" "$name"; then
    pnpm "$name"
  else
    echo "web: package script '$name' missing; skipped"
  fi
}

run_if_present check
run_if_present lint
run_if_present test
run_if_present build
