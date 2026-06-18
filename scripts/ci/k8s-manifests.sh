#!/usr/bin/env bash
set -euo pipefail

if command -v kustomize >/dev/null 2>&1; then
  build_tool="kustomize"
elif command -v kubectl >/dev/null 2>&1; then
  build_tool="kubectl"
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

rendered_any=0
for manifest_dir in deploy/base deploy/k3d; do
  if [ ! -f "$manifest_dir/kustomization.yaml" ]; then
    continue
  fi

  if [ "$build_tool" = "kustomize" ]; then
    kustomize build "$manifest_dir"
  else
    kubectl kustomize "$manifest_dir"
  fi
  echo "---"
  rendered_any=1
done >"$rendered"

if [ "$rendered_any" -eq 0 ]; then
  echo "k8s-manifests: no kustomization files initialized yet; skipped"
  exit 0
fi

if command -v kubectl >/dev/null 2>&1 && kubectl cluster-info >/dev/null 2>&1; then
  kubectl apply --dry-run=client --validate=false -f "$rendered" >/dev/null
else
  echo "k8s-manifests: kubectl cluster unavailable; rendered manifest only"
fi

echo "k8s-manifests: ok"
