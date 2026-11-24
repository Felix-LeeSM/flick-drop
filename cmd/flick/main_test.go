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
	text, err := readText([]string{"sk-live-value"}, 0)
	if err != nil {
		t.Fatalf("readText: %v", err)
	}
	if text != "sk-live-value" {
		t.Errorf("text = %q", text)
	}
}

func TestReadTextRejectsMultipleArguments(t *testing.T) {
	if _, err := readText([]string{"one", "two"}, 0); err == nil {
		t.Error("accepted two positional arguments")
	}
}

func TestReadTextReadsPipedStdin(t *testing.T) {
	withStdin(t, "sk-live-piped\n", func() {
		text, err := readText(nil, 0)
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
		if _, err := readText(nil, 0); err == nil {
			t.Error("accepted empty stdin")
		}
	})
}

func TestReadTextEnforcesInlineLimit(t *testing.T) {
	text, err := readText([]string{strings.Repeat("a", 10)}, 10)
	if err != nil {
		t.Fatalf("readText at limit: %v", err)
	}
	if len(text) != 10 {
		t.Errorf("len(text) = %d, want 10", len(text))
	}

	if _, err := readText([]string{strings.Repeat("a", 11)}, 10); err == nil {
		t.Error("accepted argument text over limit")
	}

	withStdin(t, strings.Repeat("b", 10), func() {
		text, err := readText(nil, 10)
		if err != nil {
			t.Fatalf("readText stdin at limit: %v", err)
		}
		if len(text) != 10 {
			t.Errorf("len(text) = %d, want 10", len(text))
		}
	})

	withStdin(t, strings.Repeat("b", 11), func() {
		if _, err := readText(nil, 10); err == nil {
			t.Error("accepted stdin text over limit")
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
	for _, needed := range []string{
		"flick send", "flick open", "flick request", "flick submit", "flick receive",
		"FLICK_PASSPHRASE", "FLICK_URL", "-reveal", "-credential",
	} {
		if !strings.Contains(usage, needed) {
			t.Errorf("usage text does not mention %q", needed)
		}
	}
}

func TestReadInputReadsStdinForADash(t *testing.T) {
	withStdin(t, `{"v":1}`, func() {
		data, err := readInput("-")
		if err != nil {
			t.Fatalf("readInput -: %v", err)
		}
		if string(data) != `{"v":1}` {
			t.Errorf("readInput - = %q", data)
		}
	})

	path := filepath.Join(t.TempDir(), "cred.json")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	data, err := readInput(path)
	if err != nil {
		t.Fatalf("readInput path: %v", err)
	}
	if string(data) != "from-file" {
		t.Errorf("readInput path = %q", data)
	}
}

func TestPrintOpenJSONHidesValuesUnlessRevealed(t *testing.T) {
	cred := flickcli.Credential{
		Type:  "login",
		Title: "prod",
		Fields: []flickcli.CredentialField{
			{ID: "username", Label: "Username", Value: "alice", Secret: false},
			{ID: "password", Label: "Password", Value: "hunter2", Secret: true},
		},
	}

	defaultJSON, _, err := captureOutput(t, func() error {
		return printOpenJSON(flickcli.OpenResult{Kind: "text", Credential: &cred, WrittenPath: "out.txt"}, false)
	})
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if strings.Contains(defaultJSON, "hunter2") {
		t.Errorf("default -json leaked a secret value: %s", defaultJSON)
	}
	if !strings.Contains(defaultJSON, `"saved":"out.txt"`) || !strings.Contains(defaultJSON, `"kind":"credential"`) {
		t.Errorf("default -json = %s, want the saved path and kind", defaultJSON)
	}
	if !strings.Contains(defaultJSON, `"value":"alice"`) {
		t.Errorf("default -json hid a non-secret value: %s", defaultJSON)
	}

	revealedJSON, _, err := captureOutput(t, func() error {
		return printOpenJSON(flickcli.OpenResult{Kind: "text", Credential: &cred}, true)
	})
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if !strings.Contains(revealedJSON, "hunter2") {
		t.Errorf("revealed -json lost the secret value: %s", revealedJSON)
	}

	textJSON, _, err := captureOutput(t, func() error {
		return printOpenJSON(flickcli.OpenResult{Kind: "text", Plaintext: []byte("the payload")}, true)
	})
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if !strings.Contains(textJSON, `"text":"the payload"`) {
		t.Errorf("revealed text -json = %s", textJSON)
	}
}

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

func TestPromptTTLAsksAgainInsteadOfGivingUp(t *testing.T) {
	withStdin(t, "tomorrow\n999h\n24h\n", func() {
		_, stderr, err := captureOutput(t, func() error {
			chosen, err := promptTTL(time.Hour)
			if chosen != 24*time.Hour {
				t.Errorf("ttl = %s, want 24h", chosen)
			}
			return err
		})
		if err != nil {
			t.Fatalf("promptTTL: %v", err)
		}
		if !strings.Contains(stderr, "not a duration") {
			t.Errorf("stderr did not explain the bad duration: %q", stderr)
		}
		if !strings.Contains(stderr, "A secret lives between") {
			t.Errorf("stderr did not reject an out-of-range lifetime: %q", stderr)
		}
	})
}

func TestPromptsTreatEndOfInputAsACancel(t *testing.T) {
	withStdin(t, "", func() {
		_, _, err := captureOutput(t, func() error {
			_, err := promptTTL(time.Hour)
			return err
		})
		if !errors.Is(err, errCancelled) {
			t.Errorf("promptTTL at EOF = %v, want a cancel", err)
		}
	})
	withStdin(t, "", func() {
		_, _, err := captureOutput(t, func() error {
			answered, err := promptYesNo("Protect it? [y/N]: ")
			if answered {
				t.Error("EOF was read as yes")
			}
			return err
		})
		if !errors.Is(err, errCancelled) {
			t.Errorf("promptYesNo at EOF = %v, want a cancel", err)
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

func TestRescueWritesATextSecretToAFileInsteadOfStdout(t *testing.T) {
	cause := errors.New("disk full")

	stdout, stderr, err := captureOutput(t, func() error {
		return rescue(flickcli.OpenResult{Kind: "text", Plaintext: []byte("sk-live-value")}, cause)
	})

	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to carry the cause", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no payload bytes there", stdout)
	}
	if !strings.Contains(stderr, "only remaining copy") {
		t.Errorf("stderr did not warn that this is the last copy: %q", stderr)
	}
	path := rescuedPath(t, stderr)
	defer os.Remove(path)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rescue file: %v", err)
	}
	if string(saved) != "sk-live-value" {
		t.Errorf("rescue file = %q, want the payload", saved)
	}
}

func TestRescueNamesTheRescuedFile(t *testing.T) {
	rescued := filepath.Join(t.TempDir(), ".flick-partial-1")
	if err := os.WriteFile(rescued, []byte("binary payload"), 0o600); err != nil {
		t.Fatalf("seed rescued file: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return rescue(flickcli.OpenResult{Kind: "file", RescuedPath: rescued}, errors.New("no unused filename"))
	})

	if err == nil {
		t.Fatal("rescue reported success for a secret that could not be written")
	}
	if stdout != "" {
		t.Errorf("file bytes were dumped on stdout: %q", stdout)
	}
	if got := rescuedPath(t, stderr); got != rescued {
		t.Errorf("announced %q, want %q", got, rescued)
	}
	if !strings.Contains(stderr, "only remaining copy") {
		t.Errorf("stderr did not warn that this is the last copy: %q", stderr)
	}
}

func TestRescueReportsALostFile(t *testing.T) {
	stdout, stderr, err := captureOutput(t, func() error {
		return rescue(flickcli.OpenResult{Kind: "file"}, errors.New("disk full"))
	})

	if err == nil {
		t.Fatal("rescue reported success for a secret that could not be written")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if strings.Contains(stderr, "Payload written to") || !strings.Contains(stderr, "could not be saved") {
		t.Errorf("stderr = %q, want it to report the file as lost", stderr)
	}
}

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
