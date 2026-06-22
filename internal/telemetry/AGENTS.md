# `internal/telemetry/` Guide

This package owns logs, metrics, and tracing helpers.

**Status: structured logging implemented (`log/slog`, `logging.go`); metrics and
tracing are planned (#94 phase 2).** `NewLogger` reads `FLICK_LOG_LEVEL` and
`FLICK_LOG_FORMAT`; `SetStandardLogger` routes the standard `log` package through
slog so existing `log.Printf` calls (NATS `Logf`, `net/http`) emit structured
output. The rules below describe the target behavior.

Directory structure:

- Keep logging, metrics, and tracing concerns easy to distinguish by file.
- Do not add service-specific telemetry subdirectories unless the boundary is
  stable and documented.

Rules:

- Telemetry must never include plaintext secret content, passphrases, derived
  keys, ciphertext bodies, real credentials, or private bucket names.
- Prefer stable event names and bounded labels.
- Metrics should describe system behavior without exposing user payloads or
  identifiers that can be used as secrets.
