#!/usr/bin/env bash
set -euo pipefail

project="burnlink-ci"

if ! command -v docker >/dev/null 2>&1; then
  echo "nats-smoke: docker not installed; skipped"
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  if [ "${CI:-}" = "true" ]; then
    echo "nats-smoke: docker is required in CI" >&2
    exit 1
  fi
  echo "nats-smoke: docker daemon unavailable; skipped"
  exit 0
fi

cleanup() {
  docker compose -p "$project" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose -p "$project" up -d nats

for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:8222/varz >/tmp/burnlink-nats-varz.json; then
    break
  fi
  sleep 1
done

if ! curl -fsS http://127.0.0.1:8222/varz >/tmp/burnlink-nats-varz.json; then
  docker compose -p "$project" logs nats >&2 || true
  echo "nats-smoke: NATS monitoring endpoint did not become ready" >&2
  exit 1
fi

if command -v node >/dev/null 2>&1 && node -e "process.exit(0)" >/dev/null 2>&1; then
  node -e "const v=JSON.parse(require('fs').readFileSync('/tmp/burnlink-nats-varz.json','utf8')); if (!v.jetstream) process.exit(1)"
else
  grep -q '"jetstream"' /tmp/burnlink-nats-varz.json
fi

echo "nats-smoke: ok"
