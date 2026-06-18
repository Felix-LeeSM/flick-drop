#!/usr/bin/env bash
set -euo pipefail

base="deploy/base"
kustomization="$base/kustomization.yaml"

if [ ! -f "$kustomization" ]; then
  echo "k8s-manifests: $kustomization not initialized yet; skipped"
  exit 0
fi

if command -v kustomize >/dev/null 2>&1; then
  build_cmd=(kustomize build "$base")
elif command -v kubectl >/dev/null 2>&1; then
  build_cmd=(kubectl kustomize "$base")
else
  if [ "${CI:-}" = "true" ]; then
    echo "k8s-manifests: kustomize or kubectl is required in CI" >&2
    exit 1
  fi
  echo "k8s-manifests: kustomize and kubectl not installed; skipped"
  exit 0
fi

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

"${build_cmd[@]}" >"$rendered"

if command -v kubectl >/dev/null 2>&1 && kubectl cluster-info >/dev/null 2>&1; then
  kubectl apply --dry-run=client --validate=false -f "$rendered" >/dev/null
else
  echo "k8s-manifests: kubectl cluster unavailable; rendered manifest only"
fi

echo "k8s-manifests: ok"
