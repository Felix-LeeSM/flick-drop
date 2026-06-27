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

## node-exporter — node/instance metrics

A DaemonSet (one pod per node, control-plane included via a blanket toleration)
exposing host CPU, memory, filesystem, disk, and network metrics on `:9100`. The
series reflect the node, not the pod sandbox, because it reads read-only host
`/proc`, `/sys`, and `/` mounts (`--path.*`). It runs on the pod overlay network
(not hostNetwork) so it stays scrapeable cross-node despite this cluster's
node-IP:9100 firewall. No API access, so no RBAC. Footprint ~16Mi request, 48Mi
limit per node (~3-14Mi RSS measured).

### Inspect

The Service is headless, so target a pod directly:

```sh
kubectl -n flick port-forward ds/node-exporter 19100:9100
curl -fsS http://127.0.0.1:19100/metrics | grep -E '^node_(cpu_seconds_total|memory_MemAvailable_bytes|filesystem_avail_bytes)'
```

Useful series:

- `node_cpu_seconds_total` — per-CPU time by mode (rate for utilization)
- `node_memory_MemAvailable_bytes` / `node_memory_MemTotal_bytes`
- `node_filesystem_avail_bytes` — free space per mount
- `node_load1` — 1-minute load average

## VictoriaMetrics single — storage and queries

`kube-state-metrics` and `node-exporter` only *expose* metrics; nothing stored
or queried them until this. VictoriaMetrics single-node (`vmsingle`) scrapes all
three targets, stores the series on a PVC, and serves the query API + `vmui` on
`:8428`. Full Prometheus is too heavy for a ~1GB node; vmsingle is the
low-footprint substitute (128Mi request, 384Mi limit, `--memory.allowedPercent=70`,
14-day retention).

It uses static / `dns_sd` scraping instead of Kubernetes service discovery, so it
needs **no API access and no RBAC**:

- `flick-api:8080/metrics` — bearer-guarded; the token is mounted from
  `flick-secrets` (`FLICK_METRICS_TOKEN`).
- `kube-state-metrics:8080` — stable Service DNS.
- `node-exporter` — the headless Service's A record returns every pod IP, so each
  node is scraped on the overlay network (the FQDN
  `node-exporter.flick.svc.cluster.local` assumes the `flick` namespace).

### Token setup

The `flick-api` scrape sends `Authorization: Bearer <flick-secrets/FLICK_METRICS_TOKEN>`.
With the base placeholder (`replace-me-before-use`) the API returns **401** and the
`flick-api` target stays down; set the real `FLICK_METRICS_TOKEN` in the private
overlay (the same value the API runs with). The other two targets are
unauthenticated.

### Apply and inspect

```sh
kubectl apply -k deploy/base/monitoring
kubectl -n flick rollout status deploy/vmsingle

kubectl -n flick port-forward service/vmsingle 18428:8428
# vmui dashboard:
open http://127.0.0.1:18428/vmui
# scrape target health (every target should be up=1; flick-api needs the token):
curl -fsS 'http://127.0.0.1:18428/api/v1/query?query=up' | jq '.data.result[] | {job:.metric.job, up:.value[1]}'
```

### Node placement and PVC pinning

Like the other add-ons, the base is node-agnostic; pin `vmsingle` to a worker from
a private overlay (`nodeSelector`, as shown for kube-state-metrics above) rather
than editing the base.

`vmsingle` differs in one way: it owns a **`ReadWriteOnce` PVC** (`vmsingle-data`).
On the default `local-path` StorageClass the PV binds to the node where the pod
first schedules and the data lives on that node's disk. So the overlay
`nodeSelector` and the bound PV must stay on the **same node** — if a later
reschedule moves the pod to a different node, it mounts an empty volume and loses
history. Pin the node deliberately and keep it stable, or migrate the PV with it
(see [backup and restore](../../../docs/runbook/backup-restore.md), PVC drill).

## Metrics auth

`kube-state-metrics` and `node-exporter` serve no flick secret content (only
cluster object metadata and host stats), so their `/metrics` endpoints are
unauthenticated. The flick API `/metrics` endpoint stays guarded by
`FLICK_METRICS_TOKEN`; `vmsingle` scrapes it with the mounted token, so the
guard holds end to end.
