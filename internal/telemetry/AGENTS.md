# `internal/telemetry/` Guide

This package owns logs, metrics, and tracing helpers.

**Status: structured logging (`log/slog`, `logging.go`) and Prometheus metrics
(`metrics.go`, #107 phase 2) are implemented; tracing is planned (#94 phase 3).**

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
`/metrics` by `internal/httpapi/router.go` (API and worker each serve only the
instruments their process increments). A bucket-size gauge is intentionally
omitted: computing it needs a full `ListObjectsV2` scan on every scrape, too
costly for a health endpoint — deferred until a dedicated inventory sweep is
justified.

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
- Metrics should describe system behavior without exposing user payloads or
  identifiers that can be used as secrets.
