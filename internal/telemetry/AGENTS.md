# `internal/telemetry/` Guide

This package owns logs, metrics, and tracing helpers.

**Status: structured logging (`log/slog`, `logging.go`), Prometheus metrics
(`metrics.go`, #107 phase 2), and OpenTelemetry tracing (`tracing.go`, #133
phase 3) are implemented. Cross-process trace propagation (api↔worker via the
outbox + NATS headers) is the remaining #133 PR 2 step.**

`NewLogger` reads `FLICK_LOG_LEVEL` and `FLICK_LOG_FORMAT`; `SetStandardLogger`
routes the standard `log` package through slog so existing `log.Printf` calls
(NATS `Logf`, `net/http`) emit structured output.

`metrics.go` declares process-global Prometheus instruments and registers them
on `DefaultRegistry` in `init()`:

- `flick_secret_created_total` (counter, labels `kind`, `storage`) — incremented
  in `secrets.Store.Create` (sqlite_blob) and `CreateLarge` (s3_object).
- `flick_secret_opened_total` (counter) — incremented in
  `httpapi.openAndEnqueueCleanup` after `OpenTx` commits a one-time open.
- `flick_secret_reaped_total` (counter, label `reason` `expired`|`orphan`) —
  incremented in `secrets.Reaper.ClaimOnce` after the reaper tx commits.
- `flick_jobs_processed_total` (counter, labels `kind`, `outcome`
  `succeeded`|`failed`|`dead`) — incremented in `worker.Processor.Process` /
  `finishFailed`.
- `flick_active_uploads` (gauge) — `Inc` in `CreateLarge`, `Dec` in `Finalize`
  (upload confirmed) and in the reaper (pending_upload orphan reclaimed).

`MetricsHandler()` serves the Prometheus text exposition format; mounted on
`/metrics` by `internal/httpapi/router.go` in the API process. (The worker
process increments `flick_jobs_processed_total` but does not run an HTTP
server today, so that counter is only exposed when a future worker `/metrics`
endpoint is added.) A bucket-size gauge is intentionally
omitted: computing it needs a full `ListObjectsV2` scan on every scrape, too
costly for a health endpoint — deferred until a dedicated inventory sweep is
justified.

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
(`ClaimOnce`), and worker job processing (`worker.Process`). The propagator is
W3C trace-context + baggage, ready for the PR 2 NATS-header inject/extract.

Directory structure:

- Keep logging, metrics, and tracing concerns easy to distinguish by file.
- Do not add service-specific telemetry subdirectories unless the boundary is
  stable and documented.

Rules:

- Telemetry must never include plaintext secret content, passphrases, derived
  keys, ciphertext bodies, real credentials, or private bucket names.
- Prefer stable event names and bounded labels — never IDs, ciphertext, or
  access proofs. Failed access attempts are not counted: they are an attack
  signal, not a delivery signal, and a counter would invite abuse.
- The no-IDs rule above bounds **metric label cardinality**; trace span
  attributes may carry bounded request metadata (HTTP route template, content
  kind, storage backend, status) but, like logs and metrics, never ciphertext,
  passphrases, derived keys, KDF salts, or access proofs.
- Metrics should describe system behavior without exposing user payloads or
  identifiers that can be used as secrets.
