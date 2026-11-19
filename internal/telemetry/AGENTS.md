---
type: Agent Guide
title: internal/telemetry Guide
description: Boundary, file layout, and privacy/cardinality invariants for structured logging, Prometheus metrics, and OpenTelemetry tracing in internal/telemetry.
tags: [agent-guide, go, telemetry]
---

# `internal/telemetry/` Guide

This package owns structured logging (`logging.go`), Prometheus metrics (`metrics.go`, `api_state.go`), and OpenTelemetry tracing (`tracing.go`). The full metric inventory, scrape-time database collector design, worker metrics listener, and W3C cross-process trace propagation details live in `docs/architecture/telemetry.md`.

## Directory Structure

- Keep logging (`logging.go`), metrics (`metrics.go`, `api_state.go`), and tracing (`tracing.go`) in distinct files.
- Do not add service-specific telemetry subdirectories unless the boundary is stable and documented.

## Key Implementation Rules

- `logging.go:NewLogger` reads `FLICK_LOG_LEVEL` and `FLICK_LOG_FORMAT` and wraps handlers in `stackHandler` (`NewStackHandler`), attaching goroutine stack frames (frames only, no payload data) to `Error`-level records. `SetStandardLogger` routes stdlib `log` through `slog`.
- `metrics.go` registers process-global Prometheus instruments on `DefaultRegistry` in `init()`. `MetricsHandler()` serves `/metrics` on `flick-api` (`internal/httpapi/router.go`) and on `flick-worker` (`FLICK_WORKER_METRICS_ADDR`, default `:9091`, via `NewMetricsServer`), both bearer-guarded by `FLICK_METRICS_TOKEN` (fails closed with `401` when unset).
- `api_state.go:RegisterAPIStateCollector` (wired only in `cmd/flick-api/main.go`) scrapes `flick_active_uploads`, `flick_outbox_pending`, and `flick_storage_bytes_used` from the `flick_api` PostgreSQL database with a 2-second timeout (`apiStateScrapeTimeout`), never from a `ListObjectsV2` bucket scan.
- `tracing.go:SetupTracing` installs a global OTLP/HTTP tracer provider only when `FLICK_OTLP_ENDPOINT` is set (empty leaves OTel's no-op provider with zero overhead). Traced operations use `EndSpan(span, err)` in a deferred named-return error handler; async API-to-worker propagation rides `events.JobEvent.TraceContext`, not NATS headers.

## Privacy and Cardinality Invariants

- Telemetry must never include plaintext secret content, passphrases, derived keys, ciphertext bodies, real credentials, or private bucket names.
- Prefer stable event names. Metrics describe system behavior without exposing user payloads or identifiers that can be used as secrets.
- Metric labels must stay bounded (`kind`, `storage`, `reason`, `outcome`, `limiter`) — never secret, request, or object-storage IDs, ciphertext, or access proofs.
- Failed access attempts must never get a Prometheus counter (they are an attack signal and a live counter invites abuse). Record them only in the daily aggregate `secret_open_failed` row of `usage_counters` (`internal/usage/usage.go:SecretOpenFailed`, written by `internal/secrets/store.go:Store.recordFailedAccessTx`), which has no HTTP reader.
- Trace span attributes may carry bounded request metadata (HTTP route template, content kind, storage backend, status) but, like logs and metrics, never ciphertext, passphrases, derived keys, KDF salts, or access proofs.
- Logs, metric labels, and span attributes never carry client IP addresses or delivery IDs, and no setting enables them. The API rate limiter in `internal/httpapi/ratelimit.go` keeps raw client networks in process memory only; the Valkey buckets it shares across replicas are keyed by an HMAC-SHA-256 digest under `FLICK_RATELIMIT_KEY_SALT`. The full log policy, including the nginx `flick_private` format in `web/nginx.conf` and the Loki masking in `deploy/base/monitoring/alloy.yaml`, is the `## Logs` section of `docs/architecture/security-model.md`.
