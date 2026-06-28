#!/usr/bin/env bash
set -euo pipefail

if ! command -v k3d >/dev/null 2>&1; then
  echo "k3d-smoke: k3d not installed; skipped"
  exit 0
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "k3d-smoke: kubectl not installed; skipped"
  exit 0
fi

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "k3d-smoke: docker daemon unavailable; skipped"
  exit 0
fi

if ! command -v node >/dev/null 2>&1; then
  echo "k3d-smoke: node not installed; skipped"
  exit 0
fi

cluster="${FLICK_K3D_CLUSTER:-flick-ci}"
api_port="${FLICK_K3D_API_PORT:-18080}"
web_port="${FLICK_K3D_WEB_PORT:-18081}"
tmp_dir="$(mktemp -d)"
port_forward_pids=()

cleanup() {
  for pid in "${port_forward_pids[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  done
  rm -rf "$tmp_dir"
  k3d cluster delete "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_http() {
  local url="$1"
  local attempts="${2:-60}"

  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done

  echo "k3d-smoke: timed out waiting for $url" >&2
  return 1
}

json_get() {
  local path="$1"
  node -e '
const path = process.argv[1].split(".");
let raw = "";
process.stdin.on("data", (chunk) => { raw += chunk; });
process.stdin.on("end", () => {
  const parsed = JSON.parse(raw);
  let value = parsed;
  for (const segment of path) {
    value = value?.[segment];
  }
  if (value === undefined || value === null) {
    process.exit(1);
  }
  process.stdout.write(String(value));
});
' "$path"
}

json_expect() {
  local path="$1"
  local expected="$2"
  node -e '
const path = process.argv[1].split(".");
const expected = process.argv[2];
let raw = "";
process.stdin.on("data", (chunk) => { raw += chunk; });
process.stdin.on("end", () => {
  const parsed = JSON.parse(raw);
  let value = parsed;
  for (const segment of path) {
    value = value?.[segment];
  }
  if (String(value) !== expected) {
    console.error("expected " + path.join(".") + "=" + expected + ", got " + value);
    process.exit(1);
  }
});
' "$path" "$expected"
}

start_port_forward() {
  local name="$1"
  local local_port="$2"
  local remote_port="$3"
  local log_file="$tmp_dir/$name-port-forward.log"

  kubectl -n flick port-forward --address 127.0.0.1 "service/$name" "$local_port:$remote_port" >"$log_file" 2>&1 &
  port_forward_pids+=("$!")
}

export FLICK_IMAGE_PREFIX=flick-local
export FLICK_IMAGE_TAG=ci
export FLICK_WEB_PUBLIC_API_BASE_URL=/
scripts/ci/images.sh

k3d cluster delete "$cluster" >/dev/null 2>&1 || true
k3d cluster create "$cluster" --agents 1 --wait
k3d image import \
  flick-local/flick-api:ci \
  flick-local/flick-worker:ci \
  flick-local/flick-web:ci \
  -c "$cluster"

kubectl apply -k deploy/k3d
kubectl -n flick rollout status statefulset/nats --timeout=180s
kubectl -n flick rollout status deploy/flick-api --timeout=120s
kubectl -n flick rollout status deploy/flick-worker --timeout=120s
kubectl -n flick rollout status deploy/flick-web --timeout=120s

start_port_forward flick-api "$api_port" 8080
start_port_forward flick-web "$web_port" 8080

wait_http "http://127.0.0.1:$api_port/healthz"
wait_http "http://127.0.0.1:$api_port/readyz"
wait_http "http://127.0.0.1:$web_port/healthz"

# Assert the web security response headers on the real nginx image (the only CI
# that runs it). The web is up, so the smoke runs rather than skipping.
FLICK_WEB_BASE_URL="http://127.0.0.1:$web_port" scripts/smoke/web-headers.sh

ciphertext="$(printf 'k3d-ciphertext' | base64)"
access_proof="$(printf 'k3d-proof' | base64)"
create_body="$(
  cat <<JSON
{
  "kind": "text",
  "ciphertext": "$ciphertext",
  "nonce": "$(printf 'k3d-nonce' | base64)",
  "kdf": {
    "algorithm": "PBKDF2-SHA-256",
    "salt": "$(printf 'k3d-salt' | base64)",
    "iterations": 600000,
    "key_length_bits": 256
  },
  "access": {
    "kdf": {
      "algorithm": "PBKDF2-SHA-256",
      "salt": "$(printf 'k3d-access-salt' | base64)",
      "iterations": 600000,
      "key_length_bits": 256
    },
    "proof": "$access_proof"
  },
  "size_bytes": 13,
  "ttl_seconds": 600,
  "max_views": 1
}
JSON
)"

create_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    -d "$create_body" \
    "http://127.0.0.1:$api_port/api/secrets"
)"
secret_id="$(printf '%s' "$create_response" | json_get id)"

metadata_response="$(curl -fsS "http://127.0.0.1:$api_port/api/secrets/$secret_id")"
printf '%s' "$metadata_response" | json_expect kind text

open_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    -d "{\"access_proof\":\"$access_proof\"}" \
    "http://127.0.0.1:$api_port/api/secrets/$secret_id/open"
)"
printf '%s' "$open_response" | json_expect ciphertext "$ciphertext"

echo "k3d-smoke: ok"
