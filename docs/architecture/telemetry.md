---
type: Architecture
title: Telemetry Architecture
description: Structured logging, Prometheus metrics, scrape-time database collectors, worker metrics listener, and OpenTelemetry tracing design for internal/telemetry.
tags: [architecture, telemetry, metrics, tracing, logging]
status: stable
---

# Telemetry Architecture

`internal/telemetry` owns structured logging (`logging.go`), Prometheus metrics (`metrics.go`, `api_state.go`), and OpenTelemetry distributed tracing (`tracing.go`).

## Structured Logging (`internal/telemetry/logging.go`)

- `NewLogger` reads `FLICK_LOG_LEVEL` and `FLICK_LOG_FORMAT` and wraps the JSON or text handler in `stackHandler` (`#366`), so every record at or above `Error` level carries a `stack` attribute containing goroutine stack frames only (no secret content).
- `NewStackHandler(h)` exposes that wrapping for callers building their own handler chain.
- `internal/worker/processor.go:finishFailed` is the production `slog.Error` caller: a failed or dead-lettered job attempt logs `"worker job failed"` with `kind`, `attempt`, `dead_letter`, and `err`.
- `SetStandardLogger` routes Go's standard `log` package through `slog` so `log.Printf` callers (NATS `Logf`, `net/http`) emit structured output.

## Prometheus Metrics (`internal/telemetry/metrics.go`, `api_state.go`)

Process-global instruments are declared in `metrics.go` and registered on `DefaultRegistry` in `init()`:

- `flick_secret_created_total` (counter, labels `kind`, `storage`): incremented in `secrets.Store.Create` (`storage="sqlite_blob"` for inline bytea rows) and `CreateLarge` (`storage` = the `FLICK_STORAGE_BACKENDS` name chosen by `internal/storage/registry.go:Registry.Choose`). `internal/secrets/store.go:registerCreatedSeries` pre-registers `sqlite_blob` and every configured backend name at store construction so each series exists at `0` from startup.
- `flick_secret_opened_total` (counter): incremented in `internal/httpapi/secrets.go:Server.openAndCommit` after `OpenTx` commits.
- `flick_secret_reaped_total` (counter, label `reason`: `expired`|`orphan`): incremented in `secrets.Reaper.ClaimOnce` after the reaper transaction commits.
- `flick_jobs_processed_total` (counter, labels `kind`, `outcome`: `succeeded`|`failed`|`dead`): incremented in `worker.Processor.Process` and `finishFailed`.
- `flick_request_created_total`, `flick_request_submitted_total`, `flick_request_consumed_total` (counters): incremented in `requests.Store.Create`, `Submit`, `requests/large.go:finalizeAttempt`, and `Store.Open` + `consumeObject` right after commit, so idempotent replays do not double-count. They mirror the durable `usage_counters` rows (`internal/usage`) as the scraped live view.
- `flick_outbox_published_total` (counter, label `outcome`: `published`|`failed`): incremented in `events.OutboxPublisher.PublishDue` after each batch commits.
- `flick_ratelimit_fallback_total` (counter, label `limiter`: one of the eight limiter names in `internal/httpapi/router.go:NewRouter`): incremented in `internal/httpapi/ratelimit.go:rateLimiter.allowRequest` whenever a request falls back to the in-process bucket because Valkey failed or is cooling down.
- `flick_active_uploads`, `flick_outbox_pending`, and `flick_storage_bytes_used` (gauges; `flick_storage_bytes_used` carries label `storage` bounded by `FLICK_STORAGE_BACKENDS`): not stored in process memory. `api_state.go:RegisterAPIStateCollector` (called only by `cmd/flick-api/main.go`) queries `flick_api` on every scrape with a 2-second timeout (`apiStateScrapeTimeout`) via `secrets.Store.CountPendingUploads`, `events.OutboxStore.CountPending`, and `db.StorageObjectBytes` (the database aggregate rather than a `ListObjectsV2` bucket scan, which is too costly to run on every scrape of a health endpoint). Every API replica reports the same cluster-wide value, and dashboards aggregate them with `max(...)` (`deploy/base/monitoring/grafana.yaml`). A failed read drops only that series and logs a warning.

### Metrics Exposition and Auth

- `MetricsHandler()` serves Prometheus text exposition format, mounted on `/metrics` by `internal/httpapi/router.go` in `flick-api` and on the worker's dedicated listener (`FLICK_WORKER_METRICS_ADDR`, default `:9091`) via `telemetry.NewMetricsServer`.
- Both listeners require bearer token `FLICK_METRICS_TOKEN`; an unset token fails closed (`401`).
- In Kubernetes, `deploy/base/worker.yaml` exposes the worker listener via `Service/flick-worker-metrics` and `Service/flick-worker-metrics-headless` (scraped by VictoriaMetrics). No NetworkPolicy restricts that port, so `FLICK_METRICS_TOKEN` is the in-cluster guard.

## OpenTelemetry Tracing (`internal/telemetry/tracing.go`)

- `SetupTracing` installs a global OTLP/HTTP tracer provider **only when `FLICK_OTLP_ENDPOINT` is set**; when empty, OTel's no-op provider stays active with zero overhead and no collector dependency.
- Each instrumented package holds `var tracer = otel.Tracer(<import path>)`, which delegates to the global provider even though `SetupTracing` runs later.
- `EndSpan(span, err)` records errors and ends spans; traced operations use a named-return `err` with `defer func() { telemetry.EndSpan(span, err) }()`.
- Spans cover the HTTP server (`httpapi` middleware), store (`secrets.Create`/`CreateLarge`/`Finalize`/`OpenTx`), reaper (`ClaimOnce`), and worker (`worker.Process`, `CONSUMER` span).
- **Cross-process propagation:** Uses W3C trace-context + baggage carried inside `events.JobEvent.TraceContext` (injected at outbox enqueue time while the producer span is live, extracted by `worker.Process`), rather than NATS headers, avoiding events-interface or outbox-schema changes.

## Security and Cardinality Invariants

- Telemetry must never include plaintext secret content, passphrases, derived keys, ciphertext bodies, real credentials, or private bucket names.
- Metric labels use bounded cardinality only (`kind`, `storage`, `reason`, `outcome`, `limiter`) — never secret/request/object IDs, ciphertext, or access proofs.
- Failed access attempts have no Prometheus counter (they are an attack signal and a live counter invites abuse); their only record is the daily aggregate `secret_open_failed` row in `usage_counters` (`internal/usage/usage.go:SecretOpenFailed`, written by `internal/secrets/store.go:Store.recordFailedAccessTx`), which has no HTTP reader.
- Trace span attributes may carry bounded request metadata (HTTP route template, content kind, storage backend, status) but, like logs and metrics, never ciphertext, passphrases, derived keys, KDF salts, or access proofs.
- Logs, metric labels, and span attributes never carry client IPs or delivery IDs, and no setting enables them. See the `## Logs` section of [security-model.md](security-model.md).
