// Package telemetry owns structured logging (log/slog) and, later, metrics and
// tracing. The logger must never receive secret content — callers obey
// docs/architecture/security-model.md: "Logs and metrics must not include
// plaintext, passphrases, derived keys, or ciphertext bodies."
package telemetry

import (
	"context"
	"log"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

// NewLogger builds the process logger from FLICK_LOG_LEVEL and FLICK_LOG_FORMAT.
// Defaults are level=info, format=json. Unrecognized values fall back to the
// defaults so a bad env value never blocks startup. Error-level records carry
// a stack attribute (see stackHandler), so an ERROR line in Loki shows where
// it was logged.
func NewLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("FLICK_LOG_LEVEL"))}
	handler := slog.Handler(NewStackHandler(slog.NewJSONHandler(os.Stdout, opts)))
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FLICK_LOG_FORMAT")), "text") {
		handler = NewStackHandler(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(handler)
}

// NewStackHandler wraps h so records at or above Error level carry the
// goroutine stack captured at the log call site (see stackHandler). Callers
// that build their own handler chain — tests, alternative mains — use it to
// keep the decoration NewLogger provides.
func NewStackHandler(h slog.Handler) slog.Handler {
	return stackHandler{h}
}

// stackHandler decorates every record at or above Error level with the
// goroutine stack captured at the log call site: a Loki ERROR line then shows
// where it was logged without each call site remembering to capture one.
// runtime.Stack output holds frames only — no argument or variable values —
// so it cannot leak secret content (docs/architecture/security-model.md).
// The first frames (runtime.Stack, Handle, slog's internal call chain) are
// known noise; keeping the full capture is simpler and robust to slog
// internals changing.
type stackHandler struct{ slog.Handler }

func (h stackHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		buf := make([]byte, 8192)
		n := runtime.Stack(buf, false)
		r.AddAttrs(slog.String("stack", string(buf[:n])))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup re-wrap the decorated handler: the promoted methods
// would return the inner handler, and loggers derived via logger.With would
// silently lose the stack decoration.
func (h stackHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return stackHandler{h.Handler.WithAttrs(attrs)}
}

func (h stackHandler) WithGroup(name string) slog.Handler {
	return stackHandler{h.Handler.WithGroup(name)}
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
