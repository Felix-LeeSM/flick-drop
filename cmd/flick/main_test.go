package main

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newOpenFlags mirrors the flag set runOpen builds, so the hoisting tests
// exercise the same bool/value mix the real command has.
func newOpenFlags() *flag.FlagSet {
	flags := flag.NewFlagSet("flick open", flag.ContinueOnError)
	flags.String("output", "", "")
	flags.String("output-dir", ".", "")
	flags.String("url", "", "")
	return flags
}

func newSendFlags() *flag.FlagSet {
	flags := flag.NewFlagSet("flick send", flag.ContinueOnError)
	flags.String("file", "", "")
	flags.Bool("passphrase", false, "")
	flags.Duration("ttl", time.Hour, "")
	flags.String("url", "", "")
	flags.Bool("json", false, "")
	return flags
}

// Go's flag package stops at the first non-flag argument, so flags written
// after the link were silently swallowed and reported as extra arguments.
func TestHoistFlagsMovesFlagsAheadOfPositionalArguments(t *testing.T) {
	flags := newOpenFlags()
	args := []string{"https://example.com/s/abc#key=x", "-output-dir", "out"}

	hoisted := hoistFlags(flags, args)

	want := []string{"-output-dir", "out", "https://example.com/s/abc#key=x"}
	if !slices.Equal(hoisted, want) {
		t.Fatalf("hoistFlags = %q, want %q", hoisted, want)
	}
	if err := flags.Parse(hoisted); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.NArg() != 1 {
		t.Errorf("NArg = %d, want 1", flags.NArg())
	}
	if got := flags.Lookup("output-dir").Value.String(); got != "out" {
		t.Errorf("output-dir = %q, want out", got)
	}
}

// A bool flag consumes no following token; treating it as if it did would eat
// the positional argument after it.
func TestHoistFlagsKeepsBoolFlagsSeparateFromTheirNeighbour(t *testing.T) {
	flags := newSendFlags()

	hoisted := hoistFlags(flags, []string{"my secret", "-passphrase", "-ttl", "30m"})

	if err := flags.Parse(hoisted); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.NArg() != 1 || flags.Arg(0) != "my secret" {
		t.Errorf("args = %q, want [\"my secret\"]", flags.Args())
	}
	if flags.Lookup("passphrase").Value.String() != "true" {
		t.Error("-passphrase was not set")
	}
	if got := flags.Lookup("ttl").Value.String(); got != "30m0s" {
		t.Errorf("ttl = %q, want 30m0s", got)
	}
}

func TestHoistFlagsHandlesInlineValues(t *testing.T) {
	flags := newOpenFlags()

	hoisted := hoistFlags(flags, []string{"abc123", "--output-dir=out"})

	if err := flags.Parse(hoisted); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.NArg() != 1 || flags.Arg(0) != "abc123" {
		t.Errorf("args = %q, want [abc123]", flags.Args())
	}
	if got := flags.Lookup("output-dir").Value.String(); got != "out" {
		t.Errorf("output-dir = %q, want out", got)
	}
}

// A generated password can begin with a dash, and "--" is the only way to send
// one as an argument. Dropping the terminator while hoisting made it useless.
func TestHoistFlagsPreservesTheDoubleDashTerminator(t *testing.T) {
	flags := newSendFlags()

	hoisted := hoistFlags(flags, []string{"--", "-my-secret-text"})

	if !slices.Contains(hoisted, "--") {
		t.Fatalf("hoistFlags dropped the -- terminator: %q", hoisted)
	}
	if err := flags.Parse(hoisted); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.NArg() != 1 || flags.Arg(0) != "-my-secret-text" {
		t.Errorf("args = %q, want [-my-secret-text]", flags.Args())
	}
}

func TestHoistFlagsLeavesUnknownFlagsForParseToReject(t *testing.T) {
	flags := newOpenFlags()

	hoisted := hoistFlags(flags, []string{"abc123", "-nope"})

	if err := flags.Parse(hoisted); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

func TestReadTextTakesASingleArgument(t *testing.T) {
	text, err := readText([]string{"sk-live-value"})
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if text != "sk-live-value" {
		t.Errorf("text = %q", text)
	}
}

func TestReadTextRejectsMultipleArguments(t *testing.T) {
	if _, err := readText([]string{"one", "two"}); err == nil {
		t.Error("accepted two positional arguments")
	}
}

func TestReadTextReadsPipedStdin(t *testing.T) {
	// A trailing newline belongs to the shell, not the secret: an API key
	// echoed through a pipe must not arrive with one attached.
	withStdin(t, "sk-live-piped\n", func() {
		text, err := readText(nil)
		if err != nil {
			t.Fatalf("readText: %v", err)
		}
		if text != "sk-live-piped" {
			t.Errorf("text = %q, want sk-live-piped", text)
		}
	})
}

func TestReadTextRejectsEmptyStdin(t *testing.T) {
	withStdin(t, "", func() {
		if _, err := readText(nil); err == nil {
			t.Error("accepted empty stdin")
		}
	})
}

func TestFileNameOfKeepsOnlyTheBaseName(t *testing.T) {
	for input, want := range map[string]string{
		"/home/felix/secrets/report.pdf": "report.pdf",
		"report.pdf":                     "report.pdf",
		"./nested/dir/notes.txt":         "notes.txt",
		"/":                              "flick-file",
	} {
		if got := fileNameOf(input); got != want {
			t.Errorf("fileNameOf(%q) = %q, want %q", input, got, want)
		}
	}
}

// A passphrase must never be readable from the process table, so the only
// non-interactive route is the environment.
func TestPassphraseReadersUseTheEnvironmentInsteadOfPrompting(t *testing.T) {
	t.Setenv("FLICK_PASSPHRASE", "from the environment")

	first, err := readNewPassphrase()
	if err != nil {
		t.Fatalf("readNewPassphrase: %v", err)
	}
	second, err := readExistingPassphrase()
	if err != nil {
		t.Fatalf("readExistingPassphrase: %v", err)
	}
	if first != "from the environment" || second != "from the environment" {
		t.Errorf("passphrases = %q and %q", first, second)
	}
}

func TestPassphraseReadersRejectAnEmptyEnvironmentValue(t *testing.T) {
	t.Setenv("FLICK_PASSPHRASE", "")

	if _, err := readNewPassphrase(); err == nil {
		t.Error("readNewPassphrase accepted an empty FLICK_PASSPHRASE")
	}
	if _, err := readExistingPassphrase(); err == nil {
		t.Error("readExistingPassphrase accepted an empty FLICK_PASSPHRASE")
	}
}

func TestDefaultURLPrefersTheEnvironment(t *testing.T) {
	t.Setenv("FLICK_URL", "https://flick.example.com")
	if got := defaultURL(); got != "https://flick.example.com" {
		t.Errorf("defaultURL = %q", got)
	}

	t.Setenv("FLICK_URL", "")
	if got := defaultURL(); got != defaultAPIURL {
		t.Errorf("defaultURL = %q, want the built-in default %q", got, defaultAPIURL)
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	if err := run([]string{"upload"}); err == nil {
		t.Error("accepted an unknown command")
	}
	if err := run(nil); err == nil {
		t.Error("accepted an empty command line")
	}
}

func TestClientVersionFallsBackToBuildInfo(t *testing.T) {
	// A release build stamps `version`; a `go install` build leaves it empty and
	// falls back. Neither may report nothing at all.
	if got := clientVersion(); got == "" {
		t.Error("clientVersion returned an empty string")
	}

	original := version
	t.Cleanup(func() { version = original })
	version = "v9.9.9"
	if got := clientVersion(); got != "v9.9.9" {
		t.Errorf("clientVersion = %q, want the stamped v9.9.9", got)
	}
}

func TestUsageNamesBothCommands(t *testing.T) {
	for _, needed := range []string{"flick send", "flick open", "FLICK_PASSPHRASE", "FLICK_URL"} {
		if !strings.Contains(usage, needed) {
			t.Errorf("usage text does not mention %q", needed)
		}
	}
}

// withStdin swaps os.Stdin for a pipe holding the given content, so readText
// takes its non-terminal branch.
func withStdin(t *testing.T, content string, body func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed stdin: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	defer file.Close()

	original := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = original })
	body()
}
