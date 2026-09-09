// Command flick is the Flick command-line client: it creates and opens
// one-time secrets against a Flick deployment.
//
// Like the browser, it encrypts and decrypts locally — the passphrase, the
// derived key, and the plaintext never leave this process. See
// docs/architecture/security-model.md.
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

// version is stamped at release time with -ldflags "-X main.version=...".
// A `go install` build leaves it empty and falls back to the module version
// embedded by the toolchain.
var version string

// defaultAPIURL matches web/src/lib/api/secrets.ts DEFAULT_API_BASE_URL so a
// local dev stack works with no flags.
const defaultAPIURL = "http://localhost:8080"

const usage = `flick — one-time encrypted secret links

Usage:
  flick send [flags] [text]        Create a secret and print its share link
  flick open [flags] <link|id>     Open a secret once and print or save it
  flick version                    Print the client version

Run "flick <command> -h" for the flags of a command.

Flags may go before or after the text or the link, in any order:
  flick send -ttl 24h "db password"
  flick send "db password" -ttl 24h

Run "flick send" with nothing to send and it asks for the secret, its lifetime,
and whether to protect it with a passphrase, one at a time.

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

	// Ctrl-C during a long upload should stop the request, not leave the
	// terminal hanging on a 10-minute HTTP timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "send":
		return runSend(ctx, args[1:])
	case "open":
		return runOpen(ctx, args[1:])
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

	switch {
	case *filePath != "":
		if flags.NArg() > 0 {
			return errors.New("send a file or text, not both")
		}
		payload, err := os.ReadFile(*filePath)
		if err != nil {
			return err
		}
		opts.FileBytes = payload
		opts.FileName = fileNameOf(*filePath)
	case flags.NArg() == 0 && term.IsTerminal(int(os.Stdin.Fd())):
		// Nothing to send and a human at the terminal: ask for the parts the
		// command line left out, one at a time, instead of failing with a
		// usage error. A pipe or an argument means the caller already knows
		// what they want, so nothing is asked there.
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
		text, err := readText(flags.Args())
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

	result, err := flickcli.Send(ctx, flickcli.NewClient(*apiURL, nil), opts)
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
	fmt.Fprintf(os.Stderr, "Expires %s. Opens once, then it is gone.\n", result.ExpiresAt)
	if result.Model == "passphrase" {
		fmt.Fprintln(os.Stderr, "The recipient needs the passphrase. Send it through a different channel than the link.")
	}
	return nil
}

func runOpen(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("flick open", flag.ContinueOnError)
	output := flags.String("output", "", "Write the payload to this exact path")
	outputDir := flags.String("output-dir", ".", "Directory to save a file secret into")
	apiURL := flags.String("url", "", "Flick deployment URL (default: taken from the link)")
	if err := flags.Parse(hoistFlags(flags, args)); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("give exactly one share link or secret ID")
	}

	link, err := flickcli.ParseShareLink(flags.Arg(0))
	if err != nil {
		return err
	}

	base := *apiURL
	if base == "" {
		// A deployed Flick serves /api and /s on the same origin
		// (deploy/base/ingress.yaml), so the link itself names the API.
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
	})
	if errors.Is(err, flickcli.ErrWriteAfterConsume) {
		return salvage(result, rescueDir(*output, *outputDir), err)
	}
	if err != nil {
		return err
	}

	if result.WrittenPath != "" {
		if result.FilenameUnreadable {
			// The payload is separately authenticated, so its content is
			// sound; only the name failed. Saying so is the recipient's one
			// hint that the metadata was corrupted or tampered with.
			fmt.Fprintln(os.Stderr, "flick: the sender's filename could not be decrypted; saved under a generic name.")
		}
		fmt.Fprintf(os.Stderr, "Saved %s\n", result.WrittenPath)
		return nil
	}
	_, err = os.Stdout.Write(result.Plaintext)
	if err == nil && !strings.HasSuffix(string(result.Plaintext), "\n") && term.IsTerminal(int(os.Stdout.Fd())) {
		// Keep piped output byte-exact; only a human at a terminal gets the
		// trailing newline that stops the shell prompt from running into it.
		fmt.Println()
	}
	return err
}

// salvage handles the one case where the secret no longer exists anywhere but
// this process: the server consumed it and the local write failed afterwards.
// Losing the payload here would make the client the reason a secret ceased to
// exist, so it is put somewhere the user can still reach.
//
// Text goes to stdout. A file secret does not: raw bytes written to a terminal
// are effectively unrecoverable, so it goes to a file next to the destination
// the user asked for, whose path is named on stderr.
func salvage(result flickcli.OpenResult, dir string, cause error) error {
	fmt.Fprintf(os.Stderr, "flick: %v\n", cause)
	fmt.Fprintln(os.Stderr, "The secret is gone from the server. This is the only remaining copy.")

	if result.Kind != "file" {
		fmt.Fprintln(os.Stderr, "Its contents follow on stdout — save them now.")
		if _, err := os.Stdout.Write(result.Plaintext); err != nil {
			return fmt.Errorf("%w (and the payload could not be printed: %w)", cause, err)
		}
		return cause
	}

	rescued, err := os.CreateTemp(dir, "flick-rescued-*")
	if err != nil && dir != "" {
		// That directory is a likely reason the write failed in the first
		// place, so the shared temp directory is the last resort rather than
		// the default: it is where a plaintext lingers longest unnoticed.
		rescued, err = os.CreateTemp("", "flick-rescued-*")
	}
	if err != nil {
		return fmt.Errorf("%w (and no rescue file could be created: %w)", cause, err)
	}
	defer rescued.Close()
	if _, err := rescued.Write(result.Plaintext); err != nil {
		return fmt.Errorf("%w (and the rescue file could not be written: %w)", cause, err)
	}
	fmt.Fprintf(os.Stderr, "Payload written to %s — move it somewhere safe now.\n", rescued.Name())
	return cause
}

// rescueDir names where a salvaged payload should land: beside the file the
// user asked for. os.TempDir() is a poor home for a plaintext — many systems
// do not clear it promptly, and nothing here removes the file.
func rescueDir(output, outputDir string) string {
	if output != "" {
		return filepath.Dir(output)
	}
	return outputDir
}

// readText takes the payload from the argument, or from stdin when none is
// given. Reading from stdin is what makes `... | flick send` work. A terminal
// with no argument never reaches here; runSend asks for the text instead.
func readText(args []string) (string, error) {
	if len(args) > 1 {
		return "", errors.New("give the text as one argument, or pipe it on stdin")
	}
	if len(args) == 1 {
		return args[0], nil
	}
	piped, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	// A trailing newline is the shell's, not the secret's — an API key echoed
	// through a pipe should not arrive with one attached.
	text := strings.TrimRight(string(piped), "\r\n")
	if text == "" {
		return "", errors.New("stdin was empty")
	}
	return text, nil
}

// errCancelled is Ctrl-D at a prompt: the user asked to stop, so the send is
// abandoned without an error message that reads like a failure.
var errCancelled = errors.New("cancelled")

// promptSecretText asks for the payload when `flick send` was run with nothing
// to send. It is read without echo, like a passphrase: the point of typing a
// secret at a prompt instead of passing it as an argument is that it stays out
// of the shell history, and leaving it on screen gives half of that back.
func promptSecretText() (string, error) {
	// One line, and said so: the reader stops at the first newline, so a pasted
	// PEM key would leave its remaining lines to be eaten by the next prompt.
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

// promptTTL asks how long the secret should live. Enter takes the default, so
// the common case stays one keystroke.
//
// A bad answer is asked again rather than returned as an error: the secret has
// already been typed at a prompt that does not echo it, and aborting here would
// make the user type it all over again. The contract bounds are checked here
// too, not left to flickcli.Send, so "999h" is caught before the passphrase
// prompt rather than after it.
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

// readLine reads one line from stdin a byte at a time. A bufio.Reader would
// pull whatever follows into a buffer this function then throws away, and what
// follows a prompt here is often the passphrase reader taking the terminal
// directly.
//
// Ctrl-D on an empty line is a cancel, not an empty answer: reading it as
// "take the default" would send a secret the user was trying not to send.
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

// flagGiven reports whether a flag was actually written on the command line,
// as opposed to holding its default. An interactive send asks only about what
// the caller left out.
func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// readNewPassphrase prompts twice so a typo does not produce a secret nobody
// can open. FLICK_PASSPHRASE skips the prompt for scripted use.
//
// The passphrase is never accepted as a command-line flag: arguments are
// visible to every process on the machine through ps and land in shell history.
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
	// The prompt goes to stderr so `flick open ... > secret.txt` writes only
	// the payload.
	fmt.Fprint(os.Stderr, label)
	typed, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("could not read the passphrase: %w", err)
	}
	return string(typed), nil
}

// hoistFlags moves flags ahead of positional arguments so that
// `flick open <link> -output-dir out` works. Go's flag package stops parsing at
// the first non-flag argument, which would otherwise read "-output-dir" and
// "out" as two more positional arguments and fail with a confusing count error.
//
// Whether a flag consumes the next token is asked of the FlagSet rather than
// guessed: a bool flag stands alone, every other flag takes a value.
func hoistFlags(flags *flag.FlagSet, args []string) []string {
	hoisted := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Everything after "--" is positional by convention, including
			// text that begins with a dash. The terminator itself is re-emitted
			// ahead of those arguments so the flag package still honours it —
			// dropping it would hand "-my-secret" back to Parse as a flag,
			// which is exactly what the escape hatch exists to prevent.
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

// takesNoValue reports whether a flag is boolean, which the flag package
// signals through an unexported interface on the flag's value.
func takesNoValue(flags *flag.FlagSet, name string) bool {
	found := flags.Lookup(name)
	if found == nil {
		// Unknown flags are left for Parse to reject with its own message.
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

// fileNameOf keeps only the base name. The full path is local context the
// recipient has no use for and the sender may not want to disclose.
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
