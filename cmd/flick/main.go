// The passphrase, the secret's identity, and the plaintext never leave this process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Felix-LeeSM/flick-drop/internal/flickcli"
)

// Stamped with -ldflags "-X main.version=..."; empty for `go install`, which falls back to the module version.
var version string

// Matches DEFAULT_API_BASE_URL in web/src/lib/api/secrets.ts.
const defaultAPIURL = "http://localhost:8080"

const usage = `flick — one-time encrypted secret links

Usage:
  flick send [flags] [text]           Create a secret and print its share link
  flick open [flags] <link|id>        Open a secret once and save it to a file
  flick request [flags]               Create a one-time request link pair
  flick submit [flags] <link> [text]  Send a secret into a request link
  flick receive [flags] <link|->      Retrieve the reply to a request link
  flick live [flags] [text]           Stream a secret to a nearby device by code
  flick join [flags] <code>           Receive a live transfer by its 6-digit code
  flick version                       Print the client version

Run "flick <command> -h" for the flags of a command.

Flags may go before or after the text or the link, in any order:
  flick send -ttl 24h "db password"
  flick send "db password" -ttl 24h

Run "flick send" with nothing to send and it asks for the secret, its lifetime,
and whether to protect it with a passphrase, one at a time.

A structured credential (what the web's login, card, identity, and custom
forms produce) is sent as JSON carrying "v":1, a "type", and "fields":
  flick send -credential login.json

A request link asks someone to send you a secret once: "flick request" prints
a submission link to share and a private retrieval link to keep — the retrieval
link holds the only key, so pipe it instead of leaving it in shell history:
  flick receive - < retrieval-link.txt

A live transfer streams a secret to a nearby device without storing it; the
receiver confirms themselves with a 3-digit number read out loud:
  flick live "wifi password"
  flick join 305817
Without a terminal, say how to confirm the receiver ("-confirm auto" takes the
first device, "-confirm 482" waits for that number).

An opened secret is saved to a 0600 file and only its path is printed, so
decrypted values never land in terminal scrollback or piped command logs.
Pass -reveal to print the payload itself instead: text byte-exact, a
credential as label-colon-value lines. To pipe text into a file, say
  flick open -reveal <link> > secret.txt

Text that begins with a dash needs "--" first, or it is read as a flag:
  flick send -- -----BEGIN...

Environment:
  FLICK_URL           Default deployment URL (overridden by -url)
  FLICK_PASSPHRASE    Passphrase for non-interactive use, instead of a prompt
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "flick:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "send":
		return runSend(ctx, args[1:])
	case "open":
		return runOpen(ctx, args[1:])
	case "request":
		return runRequest(ctx, args[1:])
	case "submit":
		return runSubmit(ctx, args[1:])
	case "receive":
		return runReceive(ctx, args[1:])
	case "live":
		return runLive(ctx, args[1:])
	case "join":
		return runJoin(ctx, args[1:])
	case "version":
		fmt.Println(clientVersion())
		return nil
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runSend(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick send", flag.ContinueOnError)
	filePath := flags.String("file", "", "Encrypt and send this file instead of text")
	credentialPath := flags.String("credential", "",
		`Send this credential JSON file instead of text (FLCR1 prefix optional; "-" reads stdin)`)
	askPassphrase := flags.Bool("passphrase", false,
		"Protect the secret with a passphrase the recipient must type (otherwise the key travels in the link)")
	ttl := flags.Duration("ttl", flickcli.DefaultTTL,
		fmt.Sprintf("Lifetime before the secret expires (%s to %s)", flickcli.MinTTL, flickcli.MaxTTL))
	apiURL := flags.String("url", defaultURL(), "Flick deployment URL")
	shareURL := flags.String("share-url", "",
		"Origin to render the share link with, when it differs from -url (a split dev stack)")
	asJSON := flags.Bool("json", false, "Print the result as JSON")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}

	opts := flickcli.SendOptions{TTL: *ttl, ShareOrigin: *shareURL}
	wantPassphrase := *askPassphrase

	client := flickcli.NewClient(*apiURL, nil)
	limits := client.Config(ctx)

	switch {
	case *filePath != "":
		if flags.NArg() > 0 {
			return errors.New("send a file or text, not both")
		}
		if *credentialPath != "" {
			return errors.New("send a file or a credential, not both")
		}
		file, err := os.Open(*filePath)
		if err != nil {
			return err
		}
		defer file.Close()
		opts.File = file
		opts.FileName = fileNameOf(*filePath)
	case *credentialPath != "":
		if flags.NArg() > 0 {
			return errors.New("send a credential or text, not both")
		}
		data, err := readInput(*credentialPath)
		if err != nil {
			return err
		}
		opts.Credential = string(data)
	case flags.NArg() == 0 && term.IsTerminal(int(os.Stdin.Fd())):
		text, err := promptSecretText()
		if err != nil {
			return err
		}
		opts.Text = text
		if !flagGiven(flags, "ttl") {
			if opts.TTL, err = promptTTL(*ttl); err != nil {
				return err
			}
		}
		if !wantPassphrase {
			if wantPassphrase, err = promptYesNo("Protect it with a passphrase? [y/N]: "); err != nil {
				return err
			}
		}
	default:
		text, err := readText(flags.Args(), limits.PayloadInlineMaxBytes)
		if err != nil {
			return err
		}
		opts.Text = text
	}

	if wantPassphrase {
		passphrase, err := readNewPassphrase()
		if err != nil {
			return err
		}
		opts.Passphrase = passphrase
	}

	result, err := flickcli.Send(ctx, client, opts)
	if err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"id":         result.ID,
			"link":       result.Link,
			"expires_at": result.ExpiresAt,
			"model":      result.Model,
		})
	}
	fmt.Println(result.Link)
	if t, err := time.Parse(time.RFC3339, result.ExpiresAt); err == nil {
		fmt.Fprintf(os.Stderr, "Expires %s. Opens once, then it is gone.\n", t.Format(time.RFC3339))
	} else {
		fmt.Fprintf(os.Stderr, "Expires %s. Opens once, then it is gone.\n", result.ExpiresAt)
	}
	if result.Model == "passphrase" {
		fmt.Fprintln(os.Stderr, "The recipient needs the passphrase. Send it through a different channel than the link.")
	}
	return nil
}

func runOpen(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick open", flag.ContinueOnError)
	output := flags.String("output", "", "Write the payload to this exact path")
	outputDir := flags.String("output-dir", ".", "Directory to save the payload into")
	reveal := flags.Bool("reveal", false,
		"Print the payload to stdout instead of saving it (text byte-exact; a credential as label: value lines)")
	asJSON := flags.Bool("json", false, "Print the result as JSON (values only together with -reveal)")
	apiURL := flags.String("url", "", "Flick deployment URL (default: taken from the link)")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("give exactly one share link or secret ID")
	}
	if *reveal && *output != "" {
		return errors.New("choose one destination: -reveal for stdout or -output for a file")
	}

	link, err := flickcli.ParseShareLink(flags.Arg(0))
	if err != nil {
		return err
	}

	base := *apiURL
	if base == "" {
		// A deployed Flick serves /api and /s on one origin (deploy/base/ingress.yaml).
		base = link.Origin
	}
	if base == "" {
		base = defaultURL()
	}

	result, err := flickcli.Open(ctx, flickcli.NewClient(base, nil), flickcli.OpenOptions{
		Link:       link,
		Passphrase: readExistingPassphrase,
		OutputPath: *output,
		OutputDir:  *outputDir,
		Reveal:     *reveal,
	})
	if errors.Is(err, flickcli.ErrWriteAfterConsume) {
		return rescue(result, err)
	}
	if err != nil {
		return err
	}

	if *asJSON {
		return printOpenJSON(result, *reveal)
	}
	return reportOpened(result, *reveal)
}

// The common human rendering for every opened payload: path on stdout, values
// only under reveal, masked credential summary on stderr.
func reportOpened(result flickcli.OpenResult, reveal bool) error {
	if result.WrittenPath != "" {
		if result.FilenameUnreadable {
			fmt.Fprintln(os.Stderr, "flick: the sender's filename could not be decrypted; saved under a generic name.")
		}
		// The path is the machine-readable outcome; values stay off stdout.
		fmt.Println(result.WrittenPath)
		if result.Credential != nil {
			for _, line := range result.Credential.SummaryLines() {
				fmt.Fprintln(os.Stderr, line)
			}
			fmt.Fprintln(os.Stderr, "Secret fields are masked here; the values are in the file. Run with -reveal to print them.")
		}
		return nil
	}
	if result.Credential != nil {
		fmt.Println(result.Credential.RevealText())
		return nil
	}
	_, err := os.Stdout.Write(result.Plaintext)
	if err == nil && !strings.HasSuffix(string(result.Plaintext), "\n") && term.IsTerminal(int(os.Stdout.Fd())) {
		// Keep piped output byte-exact.
		fmt.Println()
	}
	return err
}

func runRequest(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick request", flag.ContinueOnError)
	ttl := flags.Duration("ttl", flickcli.DefaultTTL,
		fmt.Sprintf("Lifetime before the request expires (%s to %s)", flickcli.MinTTL, flickcli.MaxTTL))
	note := flags.String("note", "", "Note shown to the person opening your request link (at most 1024 bytes)")
	apiURL := flags.String("url", defaultURL(), "Flick deployment URL")
	shareURL := flags.String("share-url", "",
		"Origin to render the links with, when it differs from -url (a split dev stack)")
	asJSON := flags.Bool("json", false, "Print the result as JSON")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("flick request takes no arguments, only flags")
	}

	result, err := flickcli.CreateRequest(ctx, flickcli.NewClient(*apiURL, nil), flickcli.RequestOptions{
		TTL:         *ttl,
		Note:        *note,
		ShareOrigin: *shareURL,
	})
	if err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"id":              result.ID,
			"expires_at":      result.ExpiresAt,
			"submission_link": result.SubmissionLink,
			"retrieval_link":  result.RetrievalLink,
			"note":            result.HasNote,
		})
	}
	fmt.Fprintln(os.Stderr, "Share this submission link — it works once, until it expires:")
	fmt.Println(result.SubmissionLink)
	fmt.Fprintln(os.Stderr, "Keep this private retrieval link. It carries the only key to the reply; losing it loses the reply:")
	fmt.Println(result.RetrievalLink)
	if t, err := time.Parse(time.RFC3339, result.ExpiresAt); err == nil {
		fmt.Fprintf(os.Stderr, "The request expires %s.\n", t.Format(time.RFC3339))
	} else {
		fmt.Fprintf(os.Stderr, "The request expires %s.\n", result.ExpiresAt)
	}
	return nil
}

func runSubmit(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick submit", flag.ContinueOnError)
	filePath := flags.String("file", "", "Encrypt and send this file instead of text")
	credentialPath := flags.String("credential", "",
		`Send this credential JSON file instead of text (FLCR1 prefix optional; "-" reads stdin)`)
	apiURL := flags.String("url", "", "Flick deployment URL (default: taken from the link)")
	asJSON := flags.Bool("json", false, "Print the result as JSON")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if flags.NArg() < 1 {
		return errors.New("give the submission link (and optionally the text as one argument)")
	}
	if flags.NArg() > 2 {
		return errors.New("give the link and the text as one argument, or pipe the text on stdin")
	}

	link, err := flickcli.ParseSubmissionLink(flags.Arg(0))
	if err != nil {
		return err
	}
	base := *apiURL
	if base == "" {
		base = link.Origin
	}

	if *filePath != "" && *credentialPath != "" {
		return errors.New("send a file or a credential, not both")
	}
	opts := flickcli.SubmitOptions{Link: link}
	switch {
	case *filePath != "":
		file, err := os.Open(*filePath)
		if err != nil {
			return err
		}
		defer file.Close()
		opts.File = file
		opts.FileName = fileNameOf(*filePath)
	case *credentialPath != "":
		if flags.NArg() > 1 {
			return errors.New("send a credential or text, not both")
		}
		data, err := readInput(*credentialPath)
		if err != nil {
			return err
		}
		opts.Credential = string(data)
	case flags.NArg() == 2:
		opts.Text = flags.Arg(1)
	case !term.IsTerminal(int(os.Stdin.Fd())):
		text, err := readText(nil, 0)
		if err != nil {
			return err
		}
		opts.Text = text
	default:
		text, err := promptSecretText()
		if err != nil {
			return err
		}
		opts.Text = text
	}

	result, err := flickcli.SubmitToRequest(ctx, flickcli.NewClient(base, nil), opts)
	if err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"generation": result.Generation,
			"state":      result.State,
			"note":       result.Note,
		})
	}
	if result.Note != "" {
		fmt.Fprintln(os.Stderr, "Note from the requester:")
		for _, line := range strings.Split(result.Note, "\n") {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
	}
	fmt.Fprintf(os.Stderr, "Submitted (generation %d). The requester opens it with their private link.\n", result.Generation)
	return nil
}

func runReceive(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick receive", flag.ContinueOnError)
	statusOnly := flags.Bool("status", false, "Only report the request's state; never consume the reply")
	revoke := flags.Bool("revoke", false, "Cancel the request instead of opening the reply")
	output := flags.String("output", "", "Write the reply to this exact path")
	outputDir := flags.String("output-dir", ".", "Directory to save the reply into")
	reveal := flags.Bool("reveal", false,
		"Print the reply to stdout instead of saving it (text byte-exact; a credential as label: value lines)")
	asJSON := flags.Bool("json", false, "Print the result as JSON (values only together with -reveal)")
	apiURL := flags.String("url", "", "Flick deployment URL (default: taken from the link)")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if *reveal && *output != "" {
		return errors.New("choose one destination: -reveal for stdout or -output for a file")
	}

	raw := ""
	switch {
	case flags.NArg() == 1 && flags.Arg(0) != "-":
		raw = flags.Arg(0)
	case flags.NArg() == 0 || (flags.NArg() == 1 && flags.Arg(0) == "-"):
		// The retrieval link carries the only key; stdin keeps it out of shell history.
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("give the retrieval link, or pipe it on stdin with -")
		}
		line, err := readLine()
		if err != nil {
			return err
		}
		raw = line
	default:
		return errors.New("give exactly one retrieval link")
	}

	link, err := flickcli.ParseRetrievalLink(raw)
	if err != nil {
		return err
	}
	base := *apiURL
	if base == "" {
		base = link.Origin
	}

	result, err := flickcli.ReceiveRequest(ctx, flickcli.NewClient(base, nil), flickcli.ReceiveOptions{
		Link:       link,
		OutputPath: *output,
		OutputDir:  *outputDir,
		Reveal:     *reveal,
		StatusOnly: *statusOnly,
		Revoke:     *revoke,
	})
	if result.Opened != nil && errors.Is(err, flickcli.ErrOverInlineCap) {
		fmt.Fprintln(os.Stderr, "flick: the reply passed the inline text limit; it is secured below, but check the sender.")
		return reportOpened(*result.Opened, *reveal)
	}
	if result.Opened != nil && errors.Is(err, flickcli.ErrWriteAfterConsume) {
		return rescue(*result.Opened, err)
	}
	if err != nil {
		return err
	}

	if result.Opened != nil {
		if *asJSON {
			openJSON, err := openedJSON(*result.Opened, *reveal)
			if err != nil {
				return err
			}
			openJSON["state"] = result.State
			openJSON["expires_at"] = result.ExpiresAt
			return json.NewEncoder(os.Stdout).Encode(openJSON)
		}
		return reportOpened(*result.Opened, *reveal)
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"state":      result.State,
			"expires_at": result.ExpiresAt,
		})
	}
	fmt.Fprintf(os.Stderr, "Request state: %s (expires %s).\n", result.State, result.ExpiresAt)
	switch result.State {
	case "waiting":
		fmt.Fprintln(os.Stderr, "Nothing has been submitted yet; run again later.")
	case "uploading":
		fmt.Fprintln(os.Stderr, "A submission is uploading; run again in a moment.")
	case "consumed":
		fmt.Fprintln(os.Stderr, "The reply was already opened; a request never serves it twice.")
	case "cancelled":
		fmt.Fprintln(os.Stderr, "This request was cancelled.")
	}
	return nil
}

// Values appear only under -reveal; the default shape carries the saved path and structure.
func printOpenJSON(result flickcli.OpenResult, reveal bool) error {
	out, err := openedJSON(result, reveal)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func openedJSON(result flickcli.OpenResult, reveal bool) (map[string]any, error) {
	out := map[string]any{"kind": result.Kind}
	switch {
	case result.Credential != nil:
		out["kind"] = "credential"
		out["credential"] = result.Credential.JSONMap(reveal)
		if result.WrittenPath != "" {
			out["saved"] = result.WrittenPath
		}
	case result.Kind == "file":
		out["saved"] = result.WrittenPath
		if result.Filename != "" {
			out["filename"] = result.Filename
		}
	default:
		if result.WrittenPath != "" {
			out["saved"] = result.WrittenPath
		}
		if reveal {
			out["text"] = string(result.Plaintext)
		}
	}
	return out, nil
}

// rescue saves a secret the server already consumed when its intended destination failed.
// Values go to a fresh 0600 file, never stdout; a file secret keeps its decrypted staging path.
func rescue(result flickcli.OpenResult, cause error) error {
	fmt.Fprintf(os.Stderr, "flick: %v\n", cause)

	switch {
	case result.RescuedPath != "":
		fmt.Fprintln(os.Stderr, "The secret is gone from the server. This is the only remaining copy.")
		fmt.Fprintf(os.Stderr, "Payload written to %s — move it somewhere safe now.\n", result.RescuedPath)
	case result.Kind != "file" && len(result.Plaintext) > 0:
		path, err := writeRescueFile(result.Plaintext)
		if err != nil {
			fmt.Fprintln(os.Stderr, "The secret is gone from the server, and its contents could not be saved either.")
			return fmt.Errorf("%w (and the payload could not be saved: %w)", cause, err)
		}
		fmt.Fprintln(os.Stderr, "The secret is gone from the server. This is the only remaining copy.")
		fmt.Fprintf(os.Stderr, "Payload written to %s — move it somewhere safe now.\n", path)
	default:
		fmt.Fprintln(os.Stderr, "The secret is gone from the server, and the file could not be saved while it was decrypted.")
	}
	return cause
}

// CreateTemp is already 0600; the rescue copy must not widen the payload's audience.
func writeRescueFile(plaintext []byte) (string, error) {
	file, err := os.CreateTemp("", "flick-rescue-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := file.Write(plaintext); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), file.Close()
}

// "-" reads the credential payload from stdin; a path keeps it out of shell history.
func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func readText(args []string, limit int64) (string, error) {
	if len(args) > 1 {
		return "", errors.New("give the text as one argument, or pipe it on stdin")
	}
	if len(args) == 1 {
		if limit > 0 && int64(len(args[0])) > limit {
			return "", fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(args[0]), limit)
		}
		return args[0], nil
	}
	reader := io.Reader(os.Stdin)
	if limit > 0 {
		reader = io.LimitReader(os.Stdin, limit+1)
	}
	piped, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if limit > 0 && int64(len(piped)) > limit {
		return "", fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(piped), limit)
	}
	text := strings.TrimRight(string(piped), "\r\n")
	if text == "" {
		return "", errors.New("stdin was empty")
	}
	if limit > 0 && int64(len(text)) > limit {
		return "", fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(text), limit)
	}
	return text, nil
}

var errCancelled = errors.New("cancelled")

// Read without echo, like a passphrase, so the secret stays off screen and out of shell history.
func promptSecretText() (string, error) {
	// The reader stops at the first newline, so a pasted multi-line key would leak into the next prompt.
	fmt.Fprintln(os.Stderr,
		"Type or paste the secret on one line and press Enter. It is not echoed.")
	fmt.Fprintln(os.Stderr, "For anything multi-line, use -file or pipe it in instead.")
	text, err := promptHidden("Secret: ")
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", errors.New("nothing to send")
	}
	return text, nil
}

// A bad answer is asked again: the secret was already typed without echo. Contract bounds are checked
// here so "999h" fails before the passphrase prompt.
func promptTTL(fallback time.Duration) (time.Duration, error) {
	for {
		fmt.Fprintf(os.Stderr, "Expires in [%s]: ", fallback)
		typed, err := readLine()
		if err != nil {
			return 0, err
		}
		if typed == "" {
			return fallback, nil
		}
		chosen, err := time.ParseDuration(typed)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "%q is not a duration like 30m or 24h.\n", typed)
		case chosen < flickcli.MinTTL || chosen > flickcli.MaxTTL:
			fmt.Fprintf(os.Stderr, "A secret lives between %s and %s.\n", flickcli.MinTTL, flickcli.MaxTTL)
		default:
			return chosen, nil
		}
	}
}

func promptYesNo(label string) (bool, error) {
	fmt.Fprint(os.Stderr, label)
	typed, err := readLine()
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(typed))
	return answer == "y" || answer == "yes", nil
}

// Byte at a time: a bufio.Reader would swallow input meant for the passphrase reader that follows.
// Ctrl-D on an empty line is a cancel, never "take the default".
func readLine() (string, error) {
	line := make([]byte, 0, 32)
	buf := make([]byte, 1)
	for {
		read, err := os.Stdin.Read(buf)
		if read > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return "", errCancelled
			}
			break
		}
		if err != nil {
			return "", err
		}
	}
	return strings.TrimRight(string(line), "\r"), nil
}

func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// Never accept the passphrase as a flag: arguments show up in ps and shell history.
func readNewPassphrase() (string, error) {
	if fromEnv, ok := os.LookupEnv("FLICK_PASSPHRASE"); ok {
		if fromEnv == "" {
			return "", errors.New("FLICK_PASSPHRASE is set but empty")
		}
		return fromEnv, nil
	}
	first, err := promptHidden("Passphrase: ")
	if err != nil {
		return "", err
	}
	second, err := promptHidden("Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the two passphrases do not match")
	}
	if first == "" {
		return "", errors.New("passphrase must not be empty")
	}
	return first, nil
}

func readExistingPassphrase() (string, error) {
	if fromEnv, ok := os.LookupEnv("FLICK_PASSPHRASE"); ok {
		if fromEnv == "" {
			return "", errors.New("FLICK_PASSPHRASE is set but empty")
		}
		return fromEnv, nil
	}
	return promptHidden("Passphrase: ")
}

func promptHidden(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New(
			"a passphrase is needed but stdin is not a terminal; set FLICK_PASSPHRASE instead",
		)
	}
	// stderr, so `flick open ... > secret.txt` writes only the payload.
	fmt.Fprint(os.Stderr, label)
	typed, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if errors.Is(err, io.EOF) {
		// ReadPassword reports Ctrl-D as a read error; treat it as a cancel like every other prompt.
		return "", errCancelled
	}
	if err != nil {
		return "", fmt.Errorf("could not read that: %w", err)
	}
	return string(typed), nil
}

// Go's flag package stops at the first non-flag argument, so `flick open <link> -output-dir out` needs hoisting.
func hoistFlags(flags *flag.FlagSet, args []string) []string {
	hoisted := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Re-emit "--" so Parse still treats a dash-leading secret as positional.
			positional = append(positional, args[i:]...)
			break
		}
		if len(arg) < 2 || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}

		hoisted = append(hoisted, arg)
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, _, hasInlineValue := strings.Cut(name, "=")
		if hasInlineValue || takesNoValue(flags, name) {
			continue
		}
		if i+1 < len(args) {
			i++
			hoisted = append(hoisted, args[i])
		}
	}
	return append(hoisted, positional...)
}

// The flag package signals bool flags through an unexported interface on the value.
func takesNoValue(flags *flag.FlagSet, name string) bool {
	found := flags.Lookup(name)
	if found == nil {
		return true
	}
	asBool, ok := found.Value.(interface{ IsBoolFlag() bool })
	return ok && asBool.IsBoolFlag()
}

func defaultURL() string {
	if fromEnv := os.Getenv("FLICK_URL"); fromEnv != "" {
		return fromEnv
	}
	return defaultAPIURL
}

// The full path is local context the sender may not want to disclose.
func fileNameOf(path string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return "flick-file"
	}
	return base
}

func clientVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

func runLive(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick live", flag.ContinueOnError)
	filePath := flags.String("file", "", "Encrypt and send this file instead of text")
	credentialPath := flags.String("credential", "",
		`Send this credential JSON file instead of text (FLCR1 prefix optional; "-" reads stdin)`)
	confirm := flags.String("confirm", "",
		`How to confirm the receiver: "auto" sends to the first waiting device, a 3-digit number waits for it; the default asks on a terminal`)
	apiURL := flags.String("url", defaultURL(), "Flick deployment URL")
	asJSON := flags.Bool("json", false, "Print the result as JSON")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}

	opts := flickcli.LiveSendOptions{}
	var pick func(pending []flickcli.PipePending) (flickcli.LiveDecision, error)
	switch {
	case *confirm == "auto":
	case pipeConfirmArg(*confirm) != "":
		want := pipeConfirmArg(*confirm)
		pick = func(pending []flickcli.PipePending) (flickcli.LiveDecision, error) {
			for _, conn := range pending {
				if conn.VerifyCode == want {
					return flickcli.LiveDecision{Action: "send", ConnectionID: conn.ConnectionID}, nil
				}
			}
			return flickcli.LiveDecision{Action: "wait"}, nil
		}
	case *confirm == "":
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New(
				"no terminal to confirm the receiver; pass -confirm auto, or -confirm with the 3-digit number the receiver reads out")
		}
		pick = promptLivePick
	default:
		return fmt.Errorf("-confirm must be \"auto\" or a 3-digit number, not %q", *confirm)
	}

	switch {
	case *filePath != "":
		if flags.NArg() > 0 {
			return errors.New("send a file or text, not both")
		}
		if *credentialPath != "" {
			return errors.New("send a file or a credential, not both")
		}
		file, err := os.Open(*filePath)
		if err != nil {
			return err
		}
		defer file.Close()
		opts.File = file
		opts.FileName = fileNameOf(*filePath)
	case *credentialPath != "":
		if flags.NArg() > 0 {
			return errors.New("send a credential or text, not both")
		}
		data, err := readInput(*credentialPath)
		if err != nil {
			return err
		}
		opts.Credential = string(data)
	case flags.NArg() == 1:
		opts.Text = flags.Arg(0)
	case !term.IsTerminal(int(os.Stdin.Fd())):
		text, err := readText(nil, 0)
		if err != nil {
			return err
		}
		opts.Text = text
	default:
		text, err := promptSecretText()
		if err != nil {
			return err
		}
		opts.Text = text
	}
	opts.Pick = pick
	opts.Started = func(session flickcli.PipeSession) error {
		fmt.Println(session.Code)
		fmt.Fprintf(os.Stderr, "The recipient runs: flick join %s — the code expires in %d seconds.\n", session.Code, session.ExpiresIn)
		fmt.Fprintln(os.Stderr, "Waiting for a device. Nothing is stored on the server.")
		return nil
	}

	result, err := flickcli.LiveSend(ctx, flickcli.NewClient(*apiURL, nil), opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Cancelled. The code no longer works.")
		}
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"code":              result.Code,
			"bytes_transferred": result.BytesTransferred,
			"verify_code":       result.VerifyCode,
		})
	}
	fmt.Fprintf(os.Stderr, "Sent to the device that read out %s (%d bytes). The transfer is gone from the server.\n",
		result.VerifyCode, result.BytesTransferred)
	return nil
}

// promptLivePick renders the waiting rows and reads the 3-digit number the
// receiver read out; "d<nnn>" declines that device and an empty line keeps waiting.
func promptLivePick(pending []flickcli.PipePending) (flickcli.LiveDecision, error) {
	fmt.Fprintln(os.Stderr, "Devices waiting:")
	for _, conn := range pending {
		fmt.Fprintf(os.Stderr, "  %s  %s (waiting %ds)\n", conn.VerifyCode, conn.Device, conn.ConnectedSeconds)
	}
	fmt.Fprint(os.Stderr, "Which number does your receiver see? [d<nnn> to decline, Enter to keep waiting]: ")
	typed, err := readLine()
	if err != nil {
		return flickcli.LiveDecision{}, err
	}
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return flickcli.LiveDecision{Action: "wait"}, nil
	}
	if declined, ok := strings.CutPrefix(typed, "d"); ok {
		if want := pipeConfirmArg(declined); want != "" {
			for _, conn := range pending {
				if conn.VerifyCode == want {
					return flickcli.LiveDecision{Action: "decline", ConnectionID: conn.ConnectionID}, nil
				}
			}
		}
		return flickcli.LiveDecision{Action: "wait"}, nil
	}
	if want := pipeConfirmArg(typed); want != "" {
		for _, conn := range pending {
			if conn.VerifyCode == want {
				return flickcli.LiveDecision{Action: "send", ConnectionID: conn.ConnectionID}, nil
			}
		}
		fmt.Fprintf(os.Stderr, "No device is showing %s.\n", want)
	}
	return flickcli.LiveDecision{Action: "wait"}, nil
}

func pipeConfirmArg(value string) string {
	if len(value) == 3 && strings.Trim(value, "0123456789") == "" {
		return value
	}
	return ""
}

func runJoin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick join", flag.ContinueOnError)
	output := flags.String("output", "", "Write the payload to this exact path")
	outputDir := flags.String("output-dir", ".", "Directory to save the payload into")
	reveal := flags.Bool("reveal", false,
		"Print the payload to stdout instead of saving it (text byte-exact; a credential as label: value lines)")
	asJSON := flags.Bool("json", false, "Print the result as JSON (values only together with -reveal)")
	apiURL := flags.String("url", defaultURL(), "Flick deployment URL")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("give exactly one 6-digit live transfer code")
	}
	if *reveal && *output != "" {
		return errors.New("choose one destination: -reveal for stdout or -output for a file")
	}
	code, err := flickcli.NormalizePipeCode(flags.Arg(0))
	if err != nil {
		return err
	}

	result, err := flickcli.LiveJoin(ctx, flickcli.NewClient(*apiURL, nil), flickcli.LiveJoinOptions{
		Code:       code,
		OutputPath: *output,
		OutputDir:  *outputDir,
		Reveal:     *reveal,
		Connected: func(verifyCode string, expiresIn int) error {
			fmt.Fprintf(os.Stderr, "Read this to the sender: %s\n", verifyCode)
			fmt.Fprintf(os.Stderr, "Waiting for the sender to send (expires in %d seconds)...\n", expiresIn)
			return nil
		},
	})
	if errors.Is(err, flickcli.ErrWriteAfterConsume) {
		return rescue(result, err)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Disconnected from the waiting room.")
		}
		return err
	}

	if *asJSON {
		out, err := openedJSON(result, *reveal)
		if err != nil {
			return err
		}
		out["code"] = code
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	return reportOpened(result, *reveal)
}
