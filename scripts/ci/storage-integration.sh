#!/usr/bin/env bash
set -euo pipefail

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  if [ "${CI:-}" = "true" ]; then
    echo "storage-integration: Docker is required in CI" >&2
    exit 1
  fi
  echo "storage-integration: Docker unavailable; skipped"
  exit 0
fi

# Resolve the executable before exporting fixture settings: tool-manager shims
# may reload .mise.toml and replace FLICK_S3_ENDPOINT when invoking Go.
go_binary="$(go env GOROOT)/bin/go"
project="flick-storage-ci-$(date +%s)-$$"
container="$project-minio"
image="$project:local"
tmp_dir="$(mktemp -d)"
cat >"$tmp_dir/compose.yaml" <<YAML
services:
  minio:
    image: $image
  createbuckets:
    image: $image
YAML
compose=(docker compose -f compose.yaml -f "$tmp_dir/compose.yaml" -p "$project")

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    docker logs "$container" >&2 || true
  fi
  if ! "${compose[@]}" down --volumes --remove-orphans; then
    status=1
  fi
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker build --tag "$image" - <scripts/ci/Dockerfile.minio

# compose run ignores the service's fixed host ports; Docker chooses a free
# loopback port, and the unique project owns all containers and volumes.
"${compose[@]}" run -d --no-deps --use-aliases \
  --name "$container" --publish 127.0.0.1::9000 minio
address="$(docker port "$container" 9000/tcp)"

# Never inherit a developer's real bucket or credentials for the CI fixture.
export FLICK_S3_ENDPOINT="http://$address"
export FLICK_S3_REGION=us-east-1
export FLICK_S3_BUCKET=flick-dev
export FLICK_S3_ACCESS_KEY_ID=minioadmin
export FLICK_S3_SECRET_ACCESS_KEY=minioadmin

curl --fail --silent --show-error --output /dev/null --max-time 2 \
  --retry 30 --retry-delay 1 --retry-connrefused --retry-all-errors \
  --retry-max-time 60 "$FLICK_S3_ENDPOINT/minio/health/ready"

# Fail on provisioning errors instead of the development service's retry loop.
"${compose[@]}" run --rm --no-deps -T --entrypoint /bin/sh createbuckets -ec '
  mc alias set flick http://minio:9000 minioadmin minioadmin
  mc mb --ignore-existing flick/flick-dev
'

"$go_binary" test -count=1 -timeout 2m -v -tags integration ./internal/storage/
