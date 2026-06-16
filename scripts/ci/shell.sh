#!/usr/bin/env bash
set -euo pipefail

scripts="$(find scripts -type f -name "*.sh" | sort)"

if [ -z "$scripts" ]; then
  echo "shell: no scripts found"
  exit 0
fi

while IFS= read -r script; do
  bash -n "$script"
done <<EOF
$scripts
EOF

if command -v shellcheck >/dev/null 2>&1; then
  # shellcheck disable=SC2086
  shellcheck $scripts
else
  echo "shell: shellcheck not installed; syntax-only check passed"
fi
