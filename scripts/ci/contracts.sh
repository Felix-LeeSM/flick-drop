#!/usr/bin/env bash
set -euo pipefail

if command -v node >/dev/null 2>&1 && node -e "process.exit(0)" >/dev/null 2>&1; then
  while IFS= read -r file; do
    node -e "JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8'))" "$file"
  done < <(find contracts -type f -name "*.json" | sort)
else
  echo "contracts: node unavailable; JSON parse check skipped"
fi

if [ -f contracts/openapi.yaml ]; then
  if command -v ruby >/dev/null 2>&1 && ruby -e "exit 0" >/dev/null 2>&1; then
    ruby -e "require 'yaml'; YAML.load_file(ARGV[0])" contracts/openapi.yaml
  else
    echo "contracts: ruby unavailable; openapi.yaml parse check skipped"
  fi
else
  echo "contracts: openapi.yaml not initialized yet; skipped"
fi

contract_files="$(find contracts -type f \( -name "*.json" -o -name "*.yaml" -o -name "*.yml" \) | sort)"
if [ -n "$contract_files" ] &&
  grep -InE "(decrypt[-_ ]?key|plaintext|passphrase|ciphertext_body)" $contract_files; then
  echo "contracts: forbidden sensitive payload wording found" >&2
  exit 1
fi

echo "contracts: ok"
