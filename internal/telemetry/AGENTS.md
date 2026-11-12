# `internal/telemetry/` Guide

This package owns logs, metrics, and tracing helpers.

**Status: structured logging (`log/slog`, `logging.go`), Prometheus metrics
(`metrics.go`, #107 phase 2), and OpenTelemetry tracing (`tracing.go`, #133
phase 3) are implemented, including cross-process propagation: the outbox
injects the enqueuing span's W3C trace context into the job payload
(`events.JobEvent.TraceContext`) and `worker.Process` extracts it, so a trace
continues across the async api→NATS→worker hop.**

`NewLogger` reads `FLICK_LOG_LEVEL` and `FLICK_LOG_FORMAT`; it wraps the JSON
(or text) handler in `stackHandler` (`logging.go`, #366), so every record at or
above Error level carries a `stack` attribute — the goroutine stack captured at
the log call site, frames only, so no secret content can leak through it.
`NewStackHandler(h)` (`logging.go`) exposes that wrapping for callers that
build their own handler chain. The worker job failure path
(`internal/worker/processor.go` `finishFailed`) is the production `slog.Error`
caller: a failed or dead-lettered job attempt logs `"worker job failed"` with
`kind`, `attempt`, `dead_letter`, and `err`.
`SetStandardLogger` routes the standard `log` package through slog so existing
`log.Printf` calls (NATS `Logf`, `net/http`) emit structured output.

`metrics.go` declares process-global Prometheus instruments and registers them
on `DefaultRegistry` in `init()`:

- `flick_secret_created_total` (counter, labels `kind`, `storage`) — incremented
  in `secrets.Store.Create` (`storage` `sqlite_blob`) and `CreateLarge`
  (`storage` `s3_object` or `r2_object`, the backend chosen by
  `internal/storage/registry.go:ChooseBackend`).
- `flick_secret_opened_total` (counter) — incremented in
  `internal/httpapi/secrets.go:Server.openAndCommit` after the `OpenTx`
  transaction commits a one-time open.
- `flick_secret_reaped_total` (counter, label `reason` `expired`|`orphan`) —
  incremented in `secrets.Reaper.ClaimOnce` after the reaper tx commits.
- `flick_jobs_processed_total` (counter, labels `kind`, `outcome`
  `succeeded`|`failed`|`dead`) — incremented in `worker.Processor.Process` /
  `finishFailed`.
- `flick_request_created_total` / `flick_request_submitted_total` /
  `flick_request_consumed_total` (counters) — incremented in
  `requests.Store.Create` / `Submit` (inline transition) and
  `requests/large.go` `finalizeAttempt` / `Store.Open` + `consumeObject`,
  right after the state-change transaction commits, so replayed idempotent
  paths do not count. They mirror the durable `usage_counters` rows
  (`internal/usage`) as the scraped live view.
- `flick_outbox_published_total` (counter, label `outcome`
  `published`|`failed`) and `flick_outbox_pending` (gauge) — set in
  `events.OutboxPublisher.PublishDue` after each batch; the gauge counts rows
  still in state `pending` (`OutboxStore.CountPending`), which is the queue's
  backlog as seen from its producer side.
- `flick_active_uploads` (gauge) — `Inc` in `CreateLarge`, `Dec` in `Finalize`
  (upload confirmed) and in the reaper (pending_upload orphan reclaimed).
- `flick_storage_bytes_used` (gauge, label `storage` `s3_object`|`r2_object`) —
  set from the api.db aggregate (`db.StorageObjectBytes`, see
  `telemetry.SetStorageBytesUsed`) at every placement read in
  `requests.Store.Reserve` and `secrets.Store.CreateLarge`, plus once at API
  boot (`cmd/flick-api/main.go`). It reflects what ceiling enforcement reads,
  not a bucket scan.

`MetricsHandler()` serves the Prometheus text exposition format; mounted on
`/metrics` by `internal/httpapi/router.go` in the API process, and on the
worker's dedicated listener (`FLICK_WORKER_METRICS_ADDR`, default `:9091`)
via `telemetry.NewMetricsServer` — the same bearer credential
(`FLICK_METRICS_TOKEN`) guards both, and an unset token fails closed (401).
The worker's Deployment exposes the port through the ClusterIP-only
`flick-worker-metrics` Service (`deploy/base/worker.yaml`): the port is not
exposed outside the cluster, but `deploy/` defines no NetworkPolicy, so any pod
in the cluster can connect and `FLICK_METRICS_TOKEN` is the only guard against
in-cluster readers. Bucket usage is gauged from the api.db size aggregate
(`flick_storage_bytes_used`), deliberately not from a `ListObjectsV2` scan: a
scan on every scrape is too costly for a health endpoint.

`tracing.go` wires OpenTelemetry. `SetupTracing` installs a global OTLP/HTTP
tracer provider **only when `FLICK_OTLP_ENDPOINT` is set**; empty leaves OTel's
no-op provider in place, so tracing is off with zero overhead and no collector
dependency. Each instrumented package keeps a `var tracer =
otel.Tracer(<import path>)` — the global is a delegating tracer, so a
package-level var resolves correctly even though `SetupTracing` runs later.
`EndSpan(span, err)` is the shared record-error-then-end helper; traced ops use a
named-return error + `defer func() { telemetry.EndSpan(span, err) }()` so every
error path is captured. Spans live on the HTTP server (`httpapi` middleware), the
store (`secrets.Create`/`CreateLarge`/`Finalize`/`OpenTx`), the reaper
(`ClaimOnce`), and worker job processing (`worker.Process`, a `CONSUMER` span
that continues the producer's trace). The propagator is W3C trace-context +
baggage. Cross-process propagation rides the **job payload**, not NATS headers:
`internal/events` injects the active span's context into `JobEvent.TraceContext`
at outbox enqueue time (while the span is live; the async publisher runs later)
and `worker.Process` extracts it — no events-interface or outbox-schema change.

Directory structure:

- Keep logging, metrics, and tracing concerns easy to distinguish by file.
- Do not add service-specific telemetry subdirectories unless the boundary is
  stable and documented.

Rules:

- Telemetry must never include plaintext secret content, passphrases, derived
  keys, ciphertext bodies, real credentials, or private bucket names.
- Prefer stable event names and bounded labels — never IDs, ciphertext, or
  access proofs. Failed access attempts get no Prometheus counter: they are an
  attack signal, not a delivery signal, and a live counter would invite abuse.
  Their only record is the daily aggregate `secret_open_failed` row in
  `usage_counters` (`internal/usage/usage.go:SecretOpenFailed`, written by
  `internal/secrets/store.go:Store.recordFailedAccessTx`), which has no HTTP
  reader.
- The rule against IDs in labels bounds **metric label cardinality**; trace span
  attributes may carry bounded request metadata (HTTP route template, content
  kind, storage backend, status) but, like logs and metrics, never ciphertext,
  passphrases, derived keys, KDF salts, or access proofs.
- Metrics should describe system behavior without exposing user payloads or
  identifiers that can be used as secrets.
- Logs, metric labels, and span attributes never carry client IP addresses or
  delivery IDs (secret, request, or object-storage key IDs), and no setting
  turns them on. The API rate limiter in `internal/httpapi/ratelimit.go` keeps
  client networks in memory only. The full log policy, including the nginx
  `flick_private` format in `web/nginx.conf` and the Loki masking in
  `deploy/base/monitoring/alloy.yaml`, is the `## Logs` section of
  `docs/architecture/security-model.md`.
