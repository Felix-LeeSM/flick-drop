package telemetry

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
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

// Given a logger built on stackHandler over a JSON handler,
// when an Info record and an Error record are logged,
// then only the Error line carries a non-empty goroutine stack.
func TestStackHandlerAddsStackToErrorRecordsOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(stackHandler{slog.NewJSONHandler(&buf, nil)})

	logger.Info("info line")
	logger.Error("error line", "err", "boom")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(lines), buf.String())
	}

	var info, errMsg map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &info); err != nil {
		t.Fatalf("info line is not JSON: %v\n%s", err, lines[0])
	}
	if err := json.Unmarshal([]byte(lines[1]), &errMsg); err != nil {
		t.Fatalf("error line is not JSON: %v\n%s", err, lines[1])
	}

	if _, has := info["stack"]; has {
		t.Errorf("info record has a stack attribute: %s", lines[0])
	}
	stack, has := errMsg["stack"]
	if !has {
		t.Fatalf("error record has no stack attribute: %s", lines[1])
	}
	stackStr, ok := stack.(string)
	if !ok || !strings.Contains(stackStr, "goroutine ") {
		t.Fatalf("error record stack is not a goroutine stack: %v", stack)
	}
}

// Given a logger derived via logger.WithAttrs,
// when it logs an Error record,
// then the stack decoration survives the derivation.
func TestStackHandlerSurvivesWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(stackHandler{slog.NewJSONHandler(&buf, nil)}).With("caller", "test")

	logger.Error("error line")

	line := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("line is not JSON: %v\n%s", err, line)
	}
	if _, has := rec["stack"]; !has {
		t.Errorf("derived logger's error record lost the stack attribute: %s", line)
	}
}
