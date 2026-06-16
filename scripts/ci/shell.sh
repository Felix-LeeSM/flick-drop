#!/usr/bin/env bash
set -euo pipefail

scripts=()
while IFS= read -r script; do
  scripts+=("$script")
done < <(find scripts -type f -name "*.sh" | sort)

if [ "${#scripts[@]}" -eq 0 ]; then
  echo "shell: no scripts found"
  exit 0
fi

for script in "${scripts[@]}"; do
  bash -n "$script"
done

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "${scripts[@]}"
else
  echo "shell: shellcheck not installed; syntax-only check passed"
fi
