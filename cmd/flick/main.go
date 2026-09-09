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
	default:
		text, err := readText(flags.Args())
		if err != nil {
			return err
		}
		opts.Text = text
	}

	if *askPassphrase {
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
	if err != nil {
		return err
	}

	if result.Kind == "file" {
		fmt.Fprintf(os.Stderr, "Saved %s (%s)\n", result.WrittenPath, result.ContentType)
		return nil
	}
	if *output != "" {
		// 0600: a secret written to disk should not be world-readable.
		return os.WriteFile(*output, result.Plaintext, 0o600)
	}
	_, err = os.Stdout.Write(result.Plaintext)
	if err == nil && !strings.HasSuffix(string(result.Plaintext), "\n") && term.IsTerminal(int(os.Stdout.Fd())) {
		// Keep piped output byte-exact; only a human at a terminal gets the
		// trailing newline that stops the shell prompt from running into it.
		fmt.Println()
	}
	return err
}

// readText takes the payload from the argument, or from stdin when none is
// given. Reading from stdin is what makes `... | flick send` work.
func readText(args []string) (string, error) {
	if len(args) > 1 {
		return "", errors.New("give the text as one argument, or pipe it on stdin")
	}
	if len(args) == 1 {
		return args[0], nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("nothing to send: give text as an argument, pipe it on stdin, or use -file")
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
	first, err := promptPassphrase("Passphrase: ")
	if err != nil {
		return "", err
	}
	second, err := promptPassphrase("Confirm passphrase: ")
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
	return promptPassphrase("Passphrase: ")
}

func promptPassphrase(label string) (string, error) {
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
			// text that begins with a dash.
			positional = append(positional, args[i+1:]...)
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
