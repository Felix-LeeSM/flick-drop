package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
	"github.com/Felix-LeeSM/flick-drop/internal/flickcli"
)

// sealForTest encrypts plaintext the way a sender does, to a recipient string.
func sealForTest(t *testing.T, recipient, plaintext string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := clientcrypto.EncryptToRecipient(&buf, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// freshReceiverForTest is a device about to join: the recipient it publishes
// and the way back to what was sent to it.
func freshReceiverForTest(t *testing.T) (recipient string, open func([]byte) ([]byte, error)) {
	t.Helper()
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return clientcrypto.RecipientStringOf(identity), func(raw []byte) ([]byte, error) {
		r, err := clientcrypto.DecryptWith(bytes.NewReader(raw), identity)
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}
}

// oneJSONObject fails unless text is exactly one JSON object.
func oneJSONObject(t *testing.T, text string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%q)", err, text)
	}
	if decoder.More() {
		t.Errorf("stdout carries more than one JSON object: %q", text)
	}
	return out
}

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
		"flick live", "flick join",
		"FLICK_PASSPHRASE", "FLICK_URL", "-reveal", "-credential",
	} {
		if !strings.Contains(usage, needed) {
			t.Errorf("usage text does not mention %q", needed)
		}
	}
}

func TestRunLiveJSONKeepsStdoutToOneObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprint(w, `{"payload_inline_max_bytes":1044208,"max_file_bytes":268435456}`)
		case "/api/pipe/init":
			fmt.Fprint(w, `{"code":"305817","expires_in":180,"sender_token":"sender-token"}`)
		case "/api/pipe/wait":
			// The room ended before any device joined.
			w.WriteHeader(http.StatusGone)
			fmt.Fprint(w, `{"error":{"code":"expired","message":"expired"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := captureOutput(t, func() error {
		return runLive(context.Background(), []string{"-url", server.URL, "-json", "-confirm", "auto", "payload"})
	})
	if err == nil {
		t.Fatal("an ended room reported success")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing but the final JSON object", stdout)
	}
	if !strings.Contains(stderr, "305817") {
		t.Errorf("stderr = %q, want the code a script needs before the transfer ends", stderr)
	}
}

func TestReportOpenedResultKeepsStdoutToOneObject(t *testing.T) {
	cred := flickcli.Credential{Type: "login", Fields: []flickcli.CredentialField{
		{ID: "password", Label: "Password", Value: "hunter2", Secret: true},
	}}

	stdout, _, err := captureOutput(t, func() error {
		return reportOpenedResult(flickcli.OpenResult{Kind: "text", Credential: &cred, WrittenPath: "out.txt"}, false, true,
			map[string]any{"code": "305817"})
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if decoder.More() {
		t.Errorf("stdout carried more than one JSON object: %q", stdout)
	}
	if out["code"] != "305817" || out["saved"] != "out.txt" || strings.Contains(stdout, "hunter2") {
		t.Errorf("stdout = %s, want the code and saved path without the secret value", stdout)
	}

	plain, _, err := captureOutput(t, func() error {
		return reportOpenedResult(flickcli.OpenResult{Kind: "text", Plaintext: []byte("value"), WrittenPath: "out.txt"}, false, false, nil)
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if plain != "out.txt\n" {
		t.Errorf("human stdout = %q, want only the saved path", plain)
	}
}

func TestPromptLivePickReturnsAtOnceWhenCancelled(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = original
		reader.Close()
		writer.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, stderr, err := captureOutput(t, func() error {
		// Nothing is ever written to the pipe, so only the context can end this prompt.
		_, err := promptLivePick(ctx, []flickcli.PipePending{{VerifyCode: "482", Device: "phone"}})
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("promptLivePick returned %v, want context.Canceled", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("the cancelled prompt took %s", took)
	}
	if !strings.Contains(stderr, "482") {
		t.Errorf("stderr = %q, want the waiting row", stderr)
	}
	// The line reader is still on the line; give it the Enter nobody typed.
	fmt.Fprintln(writer)
}

func TestReportOpenedSaysAFileIsSavedUnderReveal(t *testing.T) {
	stdout, stderr, err := captureOutput(t, func() error {
		return reportOpened(flickcli.OpenResult{Kind: "file", WrittenPath: "notes.txt"}, true)
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if stdout != "notes.txt\n" {
		t.Errorf("stdout = %q, want only the saved path", stdout)
	}
	if !strings.Contains(stderr, "saved, not revealed") {
		t.Errorf("stderr = %q, want the note that a file is saved under -reveal", stderr)
	}
}

// The sender side end to end: a device joins, -confirm auto picks it, and the
// relay receives bytes only that device can decrypt.
func TestRunLiveSendsToTheFirstDeviceAndPrintsOneJSONObject(t *testing.T) {
	const payload = "live payload"
	recipient, open := freshReceiverForTest(t)
	var mu sync.Mutex
	selected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pipe/send" && r.Header.Get("X-Flick-Pipe-Sender") != "sender-token" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprint(w, `{"payload_inline_max_bytes":1044208,"max_file_bytes":268435456}`)
		case "/api/pipe/init":
			fmt.Fprint(w, `{"code":"305817","expires_in":180,"sender_token":"sender-token"}`)
		case "/api/pipe/wait":
			if !selected {
				fmt.Fprintf(w, `{"status":"waiting","version":1,"connections":[{"connection_id":"c1","index":1,"verify_code":"482","device":"phone","recipient":%q,"connected_seconds":1}]}`, recipient)
				return
			}
			fmt.Fprintf(w, `{"status":"ready","version":2,"recipient":%q,"connections":[]}`, recipient)
		case "/api/pipe/select":
			selected = true
			fmt.Fprint(w, `{"status":"selected"}`)
		case "/api/pipe/send":
			raw, err := io.ReadAll(r.Body)
			got, openErr := open(raw)
			if err != nil || openErr != nil || string(got) != payload || r.Header.Get("Flick-Kind") != "text" {
				t.Errorf("the relay got a %d-byte %q stream it could not turn into the payload (err %v, open %v)",
					len(raw), r.Header.Get("Flick-Kind"), err, openErr)
				http.Error(w, "bad stream", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"status":"completed","bytes_transferred":%d}`, len(raw))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := captureOutput(t, func() error {
		return runLive(context.Background(), []string{"-url", server.URL, "-json", "-confirm", "auto", payload})
	})
	if err != nil {
		t.Fatalf("runLive: %v", err)
	}
	out := oneJSONObject(t, stdout)
	if out["code"] != "305817" || out["verify_code"] != "482" {
		t.Errorf("stdout = %s, want the code and the verify number of the chosen device", stdout)
	}
	if transferred, ok := out["bytes_transferred"].(float64); !ok || transferred == 0 {
		t.Errorf("stdout = %s, want a positive bytes_transferred", stdout)
	}
	if !strings.Contains(stderr, "305817") {
		t.Errorf("stderr = %q, want the code a script needs before the transfer ends", stderr)
	}
}

// The receiver side end to end over the inline limit: one object on stdout
// that names the saved file and the condition, and the payload kept on disk.
func TestRunJoinJSONReportsAnOverLimitPayloadAndKeepsIt(t *testing.T) {
	// Sealed, this is well past what a 512-byte inline allowance can carry.
	payload := strings.Repeat("x", 5000)
	// The relay seals to whatever recipient the device published on /join.
	joined := struct {
		sync.Mutex
		recipient string
	}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			fmt.Fprint(w, `{"payload_inline_max_bytes":512,"max_file_bytes":268435456}`)
		case "/api/pipe/join":
			var request struct {
				Recipient string `json:"recipient"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			joined.Lock()
			joined.recipient = request.Recipient
			joined.Unlock()
			fmt.Fprint(w, `{"connection_id":"c1","verify_code":"482","device":"phone","expires_in":180}`)
		case "/api/pipe/status":
			fmt.Fprint(w, `{"status":"selected"}`)
		case "/api/pipe/receive":
			joined.Lock()
			recipient := joined.recipient
			joined.Unlock()
			if recipient == "" {
				http.Error(w, "no recipient published", http.StatusConflict)
				return
			}
			w.Header().Set("Flick-Kind", "text")
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(sealForTest(t, recipient, payload))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()

	stdout, stderr, err := captureOutput(t, func() error {
		return runJoin(context.Background(), []string{"-url", server.URL, "-json", "-output-dir", dir, "305-817"})
	})
	if err != nil {
		t.Fatalf("runJoin: %v", err)
	}
	out := oneJSONObject(t, stdout)
	if out["over_inline_limit"] != true || out["kind"] != "text" || out["code"] != "305817" {
		t.Errorf("stdout = %s, want the kind, the code, and over_inline_limit", stdout)
	}
	if _, has := out["text"]; has {
		t.Errorf("stdout = %s, a value reached stdout without -reveal", stdout)
	}
	saved, _ := out["saved"].(string)
	if saved == "" {
		t.Fatalf("stdout = %s, want the saved path", stdout)
	}
	got, err := os.ReadFile(saved)
	if err != nil || string(got) != payload {
		t.Errorf("the saved reply is not the payload (err %v, %d bytes)", err, len(got))
	}
	if !strings.Contains(stderr, "inline text limit") {
		t.Errorf("stderr = %q, want the warning", stderr)
	}
}

func TestRunLiveRefusesWithoutATerminalOrConfirm(t *testing.T) {
	withStdin(t, "payload\n", func() {
		err := runLive(context.Background(), []string{"payload"})
		if err == nil || !strings.Contains(err.Error(), "-confirm") {
			t.Errorf("err = %v, want the -confirm refusal before any room is created", err)
		}
	})
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
