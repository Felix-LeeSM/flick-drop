# Monitoring (opt-in)

Lightweight, opt-in observability add-ons for the flick cluster. These are **not**
part of `deploy/base/kustomization.yaml`; apply them separately, after the base.

## kube-state-metrics — pod status

Exposes pod-status metrics in Prometheus text format so pod phase, restarts, and
readiness are visible without `kubectl` polling (and without a metrics-server,
which the base does not install).

Scoped to the pods collector only (`--resources=pods`), so RBAC is read-only on a
single object kind and the footprint stays small (~42Mi RSS measured; 32Mi
request, 96Mi limit).

### Apply

The `flick` namespace must already exist (apply `deploy/base` first).

```sh
kubectl apply -k deploy/base/monitoring
kubectl -n flick rollout status deploy/kube-state-metrics
```

### Inspect

```sh
kubectl -n flick port-forward service/kube-state-metrics 18080:8080
curl -fsS http://127.0.0.1:18080/metrics | grep -E '^kube_pod_(status_phase|status_ready|container_status_restarts_total)'
```

Useful series:

- `kube_pod_status_phase` — Pending / Running / Succeeded / Failed / Unknown
- `kube_pod_container_status_restarts_total` — restart counts per container
- `kube_pod_status_ready` — readiness condition per pod
- `kube_pod_status_reason` — eviction / OOMKilled / NodeLost reasons

### Node placement on constrained clusters

The base manifest is node-agnostic so it stays portable. On a small cluster
where the control-plane node is memory- or CPU-tight, pin this Deployment to a
worker node from a private overlay rather than editing the base:

```yaml
# overlay patch
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-state-metrics
spec:
  template:
    spec:
      nodeSelector:
        kubernetes.io/hostname: <worker-node>
```

### Metrics token

kube-state-metrics serves cluster object metadata, not flick secret content, so
its `/metrics` endpoint is unauthenticated. This is separate from the flick API
`/metrics` endpoint, which stays guarded by `FLICK_METRICS_TOKEN`.
