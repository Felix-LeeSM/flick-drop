#!/usr/bin/env bash
set -euo pipefail

if [ ! -f deploy/k3d/kustomization.yaml ]; then
  echo "k3d-smoke: deploy/k3d/kustomization.yaml not initialized yet; skipped"
  exit 0
fi

if ! command -v k3d >/dev/null 2>&1; then
  echo "k3d-smoke: k3d not installed; skipped"
  exit 0
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "k3d-smoke: kubectl not installed" >&2
  exit 1
fi

cluster="flick-ci"

cleanup() {
  k3d cluster delete "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

k3d cluster create "$cluster" --agents 1 --wait
kubectl apply -k deploy/k3d
kubectl -n flick rollout status deploy/flick-api --timeout=120s
kubectl -n flick rollout status deploy/flick-worker --timeout=120s
kubectl -n flick rollout status deploy/flick-web --timeout=120s

echo "k3d-smoke: ok"
