// Package telemetry: logs, metrics, and spans never carry secret content (docs/architecture/security-model.md).
package telemetry

import (
	"context"
	"log"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

// Unrecognized values fall back to defaults so a bad env value never blocks startup.
func NewLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("FLICK_LOG_LEVEL"))}
	handler := slog.Handler(NewStackHandler(slog.NewJSONHandler(os.Stdout, opts)))
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FLICK_LOG_FORMAT")), "text") {
		handler = NewStackHandler(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(handler)
}

func NewStackHandler(h slog.Handler) slog.Handler {
	return stackHandler{h}
}

// runtime.Stack holds frames only, no argument values, so it cannot leak secret content.
type stackHandler struct{ slog.Handler }

func (h stackHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		buf := make([]byte, 8192)
		n := runtime.Stack(buf, false)
		r.AddAttrs(slog.String("stack", string(buf[:n])))
	}
	return h.Handler.Handle(ctx, r)
}

// Re-wrap: the promoted methods would return the inner handler and drop the stack.
func (h stackHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return stackHandler{h.Handler.WithAttrs(attrs)}
}

func (h stackHandler) WithGroup(name string) slog.Handler {
	return stackHandler{h.Handler.WithGroup(name)}
}

func SetStandardLogger(logger *slog.Logger) {
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetOutput(slogLogWriter{logger: logger})
}

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
