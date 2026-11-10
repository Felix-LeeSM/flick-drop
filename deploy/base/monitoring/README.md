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
overlay (the same value the API runs with). Set it with **no trailing newline**
(e.g. `printf %s "$token"`, not `echo`): the API compares the raw token, so a
stray `\n` in the Secret value breaks the scrape. The other two targets are
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
guard holds end to end. The worker's dedicated metrics listener
(`flick-worker-metrics` Service, `deploy/base/worker.yaml`) shares the same
credential and is ClusterIP-only.

## Loki — access-log storage

Single-binary Loki storing the container logs Alloy tails through the
Kubernetes API:
filesystem store on a 5 GiB PVC, 7-day retention enforced by the compactor
(`retention_period: 168h`, `retention_enabled: true` — deleting the retention
flags turns the small PVC into an unbounded log). ClusterIP only; Grafana is
the only reader. Inspect without Grafana:

```sh
kubectl -n flick port-forward service/loki 13100:3100
curl -fsS 'http://127.0.0.1:13100/loki/api/v1/query_range' \
  --data-urlencode 'query=sum by (DownstreamStatus) (rate({namespace="kube-system", app="traefik"} | json [5m]))'
```

## Alloy — log shipper

DaemonSet tailing container logs through the Kubernetes API
(`loki.source.kubernetes` streams the `pods/log` subresource — no privileged
container, no root, no node filesystem access). Its ClusterRole grants
`pods` get/list/watch plus `pods/log` get; the `pods/log` rule is the one the
log streaming itself needs, and missing it 403s every log request while
discovery and probes stay green. A `spec.nodeName` field selector (from the
`NODE_NAME` env) scopes each replica to its own node so a 2-node cluster
stores each line once. It keeps exactly the `flick` namespace and the
`kube-system` traefik pod (one RE2-safe keep rule on `namespace/pod-name` — no
lookahead, which RE2 lacks); every other namespace is dropped before a line is
read. Traefik must emit its access log in JSON for the dashboard queries to
parse (the ops overlay enables `accessLog` with `format: json`).

## Grafana — the admin view

Serves the provisioned `Flick 사용량·큐·접속` dashboard (panels are labelled in
Korean): daily
deliveries/opens/request links from VictoriaMetrics counters, outbox backlog
and worker job outcomes from the queue series, pod phases, and the Loki
access-log panels (requests by status, top paths, unique client IPs) plus
error panels (app `level=ERROR` lines rate and raw view, traefik 4xx/5xx).
A second provisioned dashboard, `Flick 클러스터: 노드와 파드`
(`cluster.json`), shows where everything runs. Its main panel is a Treemap
nesting node → namespace → pod → container: tile area is the container's
memory *request* (16MiB minimum when none is set; actual usage is not scraped,
because cAdvisor is not a scrape target), tile colour is the container state,
and hovering a tile shows what the component does, its image, pod phase,
namespace and node. Below it sit a Nodes table (free/total memory and disk with
percentages, idle CPU, swap used), a Namespaces table, a pod-phase timeline, a
Pods table, and per-node history graphs. Node names come from joining
node-exporter's pod IP to `kube_pod_info`, so no node name is hard-coded.
Node Ready conditions are not available: `kube-state-metrics` runs with
`--resources=pods` only, so `Nodes down` counts failed node-exporter scrapes.
The Container restarts panels read high until 24 hours of samples exist,
because VictoriaMetrics `increase()` counts a new series from zero.

The Treemap panel is a plugin: `GF_INSTALL_PLUGINS=marcusolsson-treemap-panel`
makes `/run.sh` download it from grafana.com at every Grafana start (the image
runs `bash -e`, so an unreachable grafana.com crash-loops the pod until it
works). It is left unpinned because grafana.com lists no version history for
this plugin — a `@version` suffix fails with `404: Plugin not found` and
crash-loops the pod (#372). The unpinned id installs the catalog's latest
version, which the dashboards are built against. It is Grafana
Labs-maintained and Grafana-signed; the other panels do not depend on it.

Datasources
are provisioned (`vmsingle`, `loki` uids); anonymous access is disabled; the
admin credential comes from the `grafana-admin` Secret (placeholder in base —
the ops overlay must replace it before the ingress goes internet-facing).
The base ingress host is the `grafana.localhost` placeholder; the ops overlay
patches it to `grafana.<domain>` with cert-manager TLS. Grafana data is an
`emptyDir` on purpose: provisioning recreates everything except saved
dashboard edits, which do not survive a restart by design.
