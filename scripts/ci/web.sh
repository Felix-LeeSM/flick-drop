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

# The CSP (web/nginx-templates/csp.conf.template) blocks data: fonts, so the
# build must emit every font as a file. web/vite.config.ts build.assetsInlineLimit
# keeps fonts out of the inline path.
if [ -d build ] && grep -rlq 'data:font/' build; then
  echo "web: build inlines a data: font that the CSP blocks:" >&2
  grep -rl 'data:font/' build >&2
  exit 1
fi
