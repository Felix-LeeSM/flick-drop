#!/usr/bin/env bash
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
  if [ "${CI:-}" = "true" ]; then
    echo "images: docker is required in CI" >&2
    exit 1
  fi
  echo "images: docker not installed; skipped"
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  if [ "${CI:-}" = "true" ]; then
    echo "images: docker daemon is required in CI" >&2
    exit 1
  fi
  echo "images: docker daemon unavailable; skipped"
  exit 0
fi

image_prefix="${FLICK_IMAGE_PREFIX:-flick-local}"
image_tag="${FLICK_IMAGE_TAG:-ci}"
public_api_base_url="${FLICK_WEB_PUBLIC_API_BASE_URL:-/}"
public_local_file_max_bytes="${PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES:-1048560}"

docker build \
  -f Dockerfile.api \
  -t "$image_prefix/flick-api:$image_tag" \
  .

docker build \
  -f Dockerfile.worker \
  -t "$image_prefix/flick-worker:$image_tag" \
  .

docker build \
  -f web/Dockerfile \
  --build-arg "PUBLIC_FLICK_API_BASE_URL=$public_api_base_url" \
  --build-arg "PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES=$public_local_file_max_bytes" \
  -t "$image_prefix/flick-web:$image_tag" \
  web

echo "images: built $image_prefix/flick-api:$image_tag"
echo "images: built $image_prefix/flick-worker:$image_tag"
echo "images: built $image_prefix/flick-web:$image_tag"
