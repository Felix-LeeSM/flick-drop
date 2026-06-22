// Package telemetry owns structured logging (log/slog) and, later, metrics and
// tracing. The logger must never receive secret content — callers obey
// docs/architecture/security-model.md: "Logs and metrics must not include
// plaintext, passphrases, derived keys, or ciphertext bodies."
package telemetry

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds the process logger from FLICK_LOG_LEVEL and FLICK_LOG_FORMAT.
// Defaults are level=info, format=json. Unrecognized values fall back to the
// defaults so a bad env value never blocks startup.
func NewLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("FLICK_LOG_LEVEL"))}
	handler := slog.Handler(slog.NewJSONHandler(os.Stdout, opts))
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FLICK_LOG_FORMAT")), "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// SetStandardLogger routes the standard library's log package and the default
// slog through logger, so existing log.Printf calls (including NATS Logf and the
// net/http server logs) emit structured output until they migrate to slog
// directly. No secret redaction happens here — callers must not log secrets.
func SetStandardLogger(logger *slog.Logger) {
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetOutput(slogLogWriter{logger: logger})
}

// slogLogWriter adapts the standard library's log package to slog: each
// log.Printf line becomes an Info-level slog record, so existing callers
// (NATS Logf, net/http) emit structured JSON without code changes.
type slogLogWriter struct{ logger *slog.Logger }

func (w slogLogWriter) Write(p []byte) (int, error) {
	w.logger.Info(strings.TrimSpace(string(p)))
	return len(p), nil
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default: // "" and "info"
		return slog.LevelInfo
	}
}
