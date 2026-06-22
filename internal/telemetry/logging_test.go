package telemetry

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"info":     slog.LevelInfo,
		"INFO":     slog.LevelInfo,
		"debug":    slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"garbage":  slog.LevelInfo,
		"  DEBUG ": slog.LevelDebug,
	}
	for raw, want := range cases {
		if got := parseLevel(raw); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", raw, got, want)
		}
	}
}
