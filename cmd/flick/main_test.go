package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/flickcli"
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

// Running `flick send` with nothing to send used to be a usage error. It now
// asks, and Enter must keep the default lifetime so the common case stays one
// keystroke.
func TestPromptTTLTakesTheDefaultOnAnEmptyLine(t *testing.T) {
	withStdin(t, "\n", func() {
		_, _, err := captureOutput(t, func() error {
			chosen, err := promptTTL(time.Hour)
			if chosen != time.Hour {
				t.Errorf("ttl = %s, want the 1h default", chosen)
			}
			return err
		})
		if err != nil {
			t.Fatalf("promptTTL: %v", err)
		}
	})
}

func TestPromptTTLReadsADurationAndRejectsGarbage(t *testing.T) {
	withStdin(t, "24h\n", func() {
		_, _, err := captureOutput(t, func() error {
			chosen, err := promptTTL(time.Hour)
			if chosen != 24*time.Hour {
				t.Errorf("ttl = %s, want 24h", chosen)
			}
			return err
		})
		if err != nil {
			t.Fatalf("promptTTL: %v", err)
		}
	})

	withStdin(t, "tomorrow\n", func() {
		_, _, err := captureOutput(t, func() error {
			_, err := promptTTL(time.Hour)
			return err
		})
		if err == nil {
			t.Error("promptTTL accepted a value that is not a duration")
		}
	})
}

func TestPromptYesNoOnlyAcceptsYes(t *testing.T) {
	for typed, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true,
		"\n": false, "n\n": false, "sure\n": false,
	} {
		withStdin(t, typed, func() {
			_, _, err := captureOutput(t, func() error {
				answered, err := promptYesNo("Protect it? [y/N]: ")
				if answered != want {
					t.Errorf("promptYesNo(%q) = %v, want %v", typed, answered, want)
				}
				return err
			})
			if err != nil {
				t.Fatalf("promptYesNo: %v", err)
			}
		})
	}
}

// readLine must consume exactly one line: the next reader on this terminal is
// often the passphrase prompt, and a buffered reader would have swallowed it.
func TestReadLineLeavesTheRestOfStdinForTheNextReader(t *testing.T) {
	withStdin(t, "24h\nthe next answer\n", func() {
		first, err := readLine()
		if err != nil {
			t.Fatalf("readLine: %v", err)
		}
		second, err := readLine()
		if err != nil {
			t.Fatalf("readLine: %v", err)
		}
		if first != "24h" || second != "the next answer" {
			t.Errorf("lines = %q and %q", first, second)
		}
	})
}

// A flag that was actually typed must not be asked about again.
func TestFlagGivenDistinguishesATypedFlagFromItsDefault(t *testing.T) {
	flags := newSendFlags()
	if err := flags.Parse([]string{"-ttl", "1h"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !flagGiven(flags, "ttl") {
		t.Error("a typed -ttl was reported as absent")
	}
	if flagGiven(flags, "passphrase") {
		t.Error("an untouched -passphrase was reported as given")
	}
}

// salvage is the last stop for a secret the server has already destroyed: if it
// does not reach the user here, nothing else will. Text goes to stdout so a
// redirect still catches it.
func TestSalvagePrintsATextSecretOnStdout(t *testing.T) {
	dir := t.TempDir()
	cause := errors.New("disk full")

	stdout, stderr, err := captureOutput(t, func() error {
		return salvage(flickcli.OpenResult{Kind: "text", Plaintext: []byte("sk-live-value")}, dir, cause)
	})

	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to carry the cause", err)
	}
	if stdout != "sk-live-value" {
		t.Errorf("stdout = %q, want the payload", stdout)
	}
	if !strings.Contains(stderr, "only remaining copy") {
		t.Errorf("stderr did not warn that this is the last copy: %q", stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a text salvage wrote files: %v", entries)
	}
}

// Raw file bytes on a terminal are unrecoverable, so a file secret goes to
// disk — next to where the user asked for it, not into the shared temp
// directory that nothing clears.
func TestSalvageWritesAFileSecretBesideTheRequestedOutput(t *testing.T) {
	dir := t.TempDir()

	stdout, stderr, err := captureOutput(t, func() error {
		return salvage(flickcli.OpenResult{Kind: "file", Plaintext: []byte("binary payload")}, dir, errors.New("disk full"))
	})

	if err == nil {
		t.Fatal("salvage reported success for a secret that could not be written")
	}
	if stdout != "" {
		t.Errorf("file bytes were dumped on stdout: %q", stdout)
	}
	rescued := rescuedPath(t, stderr)
	if filepath.Dir(rescued) != dir {
		t.Errorf("rescued file = %s, want it inside %s", rescued, dir)
	}
	written, readErr := os.ReadFile(rescued)
	if readErr != nil {
		t.Fatalf("read rescued file: %v", readErr)
	}
	if string(written) != "binary payload" {
		t.Errorf("rescued payload = %q", written)
	}
	info, statErr := os.Stat(rescued)
	if statErr != nil {
		t.Fatalf("stat rescued file: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("rescued file mode = %v, want 0600", info.Mode().Perm())
	}
}

// An unusable output directory is the likeliest reason the write failed in the
// first place, so it must not take the payload down with it.
func TestSalvageFallsBackToTheTempDirectoryWhenTheOutputDirectoryIsUnusable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")

	_, stderr, err := captureOutput(t, func() error {
		return salvage(flickcli.OpenResult{Kind: "file", Plaintext: []byte("binary payload")}, missing, errors.New("disk full"))
	})

	if err == nil {
		t.Fatal("salvage reported success for a secret that could not be written")
	}
	rescued := rescuedPath(t, stderr)
	t.Cleanup(func() { os.Remove(rescued) })
	written, readErr := os.ReadFile(rescued)
	if readErr != nil {
		t.Fatalf("read rescued file: %v", readErr)
	}
	if string(written) != "binary payload" {
		t.Errorf("rescued payload = %q", written)
	}
}

func TestRescueDirPrefersTheDirectoryOfTheRequestedOutput(t *testing.T) {
	if got := rescueDir(filepath.Join("out", "secret.txt"), "."); got != "out" {
		t.Errorf("rescueDir with -output = %q, want out", got)
	}
	if got := rescueDir("", "downloads"); got != "downloads" {
		t.Errorf("rescueDir without -output = %q, want downloads", got)
	}
}

// rescuedPath reads back the path salvage announced on stderr, which is the
// only way the user learns where their secret went.
func rescuedPath(t *testing.T, stderr string) string {
	t.Helper()
	_, after, found := strings.Cut(stderr, "Payload written to ")
	if !found {
		t.Fatalf("stderr never named the rescue file: %q", stderr)
	}
	path, _, found := strings.Cut(after, " —")
	if !found {
		t.Fatalf("stderr did not terminate the rescue path: %q", stderr)
	}
	return path
}

// captureOutput redirects os.Stdout and os.Stderr into files for the duration
// of body, so the payload salvage prints can be asserted on.
func captureOutput(t *testing.T, body func() error) (stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()
	outFile, stdoutErr := os.Create(filepath.Join(dir, "stdout"))
	errFile, stderrErr := os.Create(filepath.Join(dir, "stderr"))
	if stdoutErr != nil || stderrErr != nil {
		t.Fatalf("create capture files: %v %v", stdoutErr, stderrErr)
	}
	defer outFile.Close()
	defer errFile.Close()

	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	err = body()
	os.Stdout, os.Stderr = originalOut, originalErr

	return readFile(t, outFile.Name()), readFile(t, errFile.Name()), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
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
