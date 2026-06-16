#!/usr/bin/env bash
set -euo pipefail

if [ ! -f go.mod ]; then
  echo "go: go.mod not initialized yet; skipped"
  exit 0
fi

go_files="$(find . -type f -name "*.go" -not -path "./var/*" | sort)"
if [ -n "$go_files" ]; then
  # shellcheck disable=SC2086
  unformatted="$(gofmt -l $go_files)"
  if [ -n "$unformatted" ]; then
    echo "go: gofmt required for:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
fi

go vet ./...
go test ./...
