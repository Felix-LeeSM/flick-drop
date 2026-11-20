#!/usr/bin/env bash
set -euo pipefail

# Mirrors of internal/config/defaults.go TTLs (web/Dockerfile, publish-images.yml, OpenAPI) must match.

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
defaults="$root/internal/config/defaults.go"
dockerfile="$root/web/Dockerfile"
publish="$root/.github/workflows/publish-images.yml"
openapi="$root/contracts/openapi.yaml"

extract() {
  local pattern="$1" file="$2"
  grep -Eo "$pattern" "$file" | grep -Eo '[0-9]+' | head -1
}

go_min=$(extract 'defaultMinTTLSeconds[[:space:]]+int[[:space:]]*=[[:space:]]*[0-9]+' "$defaults")
go_max=$(extract 'defaultMaxTTLSeconds[[:space:]]+int[[:space:]]*=[[:space:]]*[0-9]+' "$defaults")
go_default=$(extract 'defaultDefaultTTLSeconds[[:space:]]+int[[:space:]]*=[[:space:]]*[0-9]+' "$defaults")

failed=0
check() {
  local label="$1" expected="$2" actual="$3" where="$4"
  if [ -z "$actual" ]; then
    echo "ttl-drift: $label not found in $where" >&2
    failed=1
  elif [ "$actual" != "$expected" ]; then
    echo "ttl-drift: $label in $where is $actual, expected $expected (from defaults.go)" >&2
    failed=1
  fi
}

check MIN "$go_min" "$(extract 'ARG PUBLIC_FLICK_MIN_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"
check MAX "$go_max" "$(extract 'ARG PUBLIC_FLICK_MAX_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"
check DEFAULT "$go_default" "$(extract 'ARG PUBLIC_FLICK_DEFAULT_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"

check MIN "$go_min" "$(extract 'PUBLIC_FLICK_MIN_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"
check MAX "$go_max" "$(extract 'PUBLIC_FLICK_MAX_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"
check DEFAULT "$go_default" "$(extract 'PUBLIC_FLICK_DEFAULT_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"

# The contract carries no default.
check MIN "$go_min" "$(extract "minimum:[[:space:]]*${go_min}([^0-9]|$)" "$openapi")" "$openapi (ttl_seconds minimum)"
check MAX "$go_max" "$(extract "maximum:[[:space:]]*${go_max}([^0-9]|$)" "$openapi")" "$openapi (ttl_seconds maximum)"

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "ttl-drift: ok (min=$go_min max=$go_max default=$go_default)"
