#!/usr/bin/env bash
set -euo pipefail

# Assert the TTL defaults (min/max/default seconds) are identical everywhere
# they're mirrored. The Go config is the source of truth
# (internal/config/defaults.go); the web build pipeline (web/Dockerfile,
# publish-images.yml) and the OpenAPI ttl_seconds bounds must track it. Fails CI
# if any mirror drifts — bumping a Go default without updating the web build
# surfaces here (issue #87 AC#3).

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
defaults="$root/internal/config/defaults.go"
dockerfile="$root/web/Dockerfile"
publish="$root/.github/workflows/publish-images.yml"
openapi="$root/contracts/openapi.yaml"

extract() {
  # Print the first integer matched by the grep -E pattern, or empty.
  local pattern="$1" file="$2"
  grep -Eo "$pattern" "$file" | grep -Eo '[0-9]+' | head -1
}

# Source of truth.
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

# web/Dockerfile ARG defaults.
check MIN "$go_min" "$(extract 'ARG PUBLIC_FLICK_MIN_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"
check MAX "$go_max" "$(extract 'ARG PUBLIC_FLICK_MAX_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"
check DEFAULT "$go_default" "$(extract 'ARG PUBLIC_FLICK_DEFAULT_TTL_SECONDS=[0-9]+' "$dockerfile")" "$dockerfile"

# publish-images.yml env defaults (quoted strings).
check MIN "$go_min" "$(extract 'PUBLIC_FLICK_MIN_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"
check MAX "$go_max" "$(extract 'PUBLIC_FLICK_MAX_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"
check DEFAULT "$go_default" "$(extract 'PUBLIC_FLICK_DEFAULT_TTL_SECONDS:[[:space:]]*"[0-9]+"' "$publish")" "$publish"

# OpenAPI ttl_seconds bounds (min/max only — the contract carries no default).
check MIN "$go_min" "$(extract "minimum:[[:space:]]*${go_min}([^0-9]|$)" "$openapi")" "$openapi (ttl_seconds minimum)"
check MAX "$go_max" "$(extract "maximum:[[:space:]]*${go_max}([^0-9]|$)" "$openapi")" "$openapi (ttl_seconds maximum)"

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "ttl-drift: ok (min=$go_min max=$go_max default=$go_default)"
