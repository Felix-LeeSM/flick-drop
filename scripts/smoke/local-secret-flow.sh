#!/usr/bin/env bash
set -euo pipefail

api_base="${BURNLINK_API_BASE_URL:-http://localhost:8080}"

if ! curl -fsS "$api_base/healthz" >/dev/null 2>&1; then
  echo "local-secret-flow: API not reachable at $api_base; skipped"
  exit 0
fi

echo "local-secret-flow: API reachable"
echo "local-secret-flow: implement create/open/consume checks after API routes exist"
