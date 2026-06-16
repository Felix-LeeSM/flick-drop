#!/usr/bin/env bash
set -euo pipefail

example=".env.example"
doc="docs/architecture/env-contract.md"

if [ ! -f "$example" ]; then
  echo "env-contract: missing $example" >&2
  exit 1
fi

if [ ! -f "$doc" ]; then
  echo "env-contract: missing $doc" >&2
  exit 1
fi

missing=0
while IFS= read -r key; do
  if ! grep -q "\`$key\`" "$doc"; then
    echo "env-contract: $key is in $example but not documented in $doc" >&2
    missing=1
  fi
done < <(grep -E "^BURNLINK_[A-Z0-9_]+=" "$example" | cut -d= -f1 | sort -u)

if ! grep -q "^\\.env\\.\\*$" .gitignore; then
  echo "env-contract: .gitignore must ignore .env.*" >&2
  missing=1
fi

if ! grep -q "^!\\.env\\.example$" .gitignore; then
  echo "env-contract: .gitignore must allow .env.example" >&2
  missing=1
fi

if [ "$missing" -ne 0 ]; then
  exit 1
fi

echo "env-contract: ok"
