#!/usr/bin/env bash
set -euo pipefail

if [ ! -f deploy/base/kustomization.yaml ]; then
  echo "k3d-smoke: deploy/base/kustomization.yaml not initialized yet; skipped"
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

cluster="burnlink-ci"

cleanup() {
  k3d cluster delete "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

k3d cluster create "$cluster" --agents 1 --wait
kubectl apply -k deploy/base
kubectl -n burnlink rollout status deploy/burnlink-api --timeout=120s
kubectl -n burnlink rollout status deploy/burnlink-worker --timeout=120s
kubectl -n burnlink rollout status deploy/burnlink-web --timeout=120s

echo "k3d-smoke: ok"
