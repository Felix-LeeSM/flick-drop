package flickcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// PassphraseFunc supplies a passphrase on demand. It is called only when the
// secret turns out to be Model A, so opening a link-key secret never prompts.
type PassphraseFunc func() (string, error)

// OpenOptions describes one open. The open is destructive on the server side:
// a successful call consumes the secret, and there is no second chance.
type OpenOptions struct {
	Link ShareLink
	// Passphrase is called only for Model A secrets, after the metadata probe
	// and before the consuming open.
	Passphrase PassphraseFunc
	// OutputPath writes the payload to this exact path, for a text or a file
	// secret alike. Empty means text goes to the caller's stdout and a file is
	// written under its decrypted name in OutputDir.
	OutputPath string
	// OutputDir is where a file secret lands when OutputPath is empty.
	OutputDir string
}

// OpenResult reports what was recovered. Plaintext is always populated, even
// when writing it out failed, so a caller can still salvage a payload the
// server has already destroyed.
type OpenResult struct {
	Kind        string
	Plaintext   []byte
	Filename    string
	ContentType string
	// WrittenPath is the file actually written, which may differ from the
	// requested name when a collision was avoided. Empty means nothing was
	// written and the caller owns the payload.
	WrittenPath string
}

// ErrWriteAfterConsume marks the one unrecoverable-looking case: the secret was
// consumed on the server and then could not be written locally. The payload is
// still in the returned OpenResult.Plaintext, and the caller must surface it
// rather than discard it — the server has no second copy.
var ErrWriteAfterConsume = errors.New("secret was opened and consumed but could not be written")

// Open consumes the secret and decrypts it locally.
//
// Order matters and is not incidental. Everything that can fail is done before
// the consuming open: the metadata probe, the passphrase prompt, the check that
// a link-key secret actually carries its key, and — because a secret destroyed
// on the server and then dropped on the floor locally is the worst outcome this
// client has — reserving the output destination. Once OpenSecret returns, the
// payload exists only in this process.
func Open(ctx context.Context, client *Client, opts OpenOptions) (OpenResult, error) {
	if opts.Link.ID == "" {
		return OpenResult{}, errors.New("no secret ID to open")
	}

	metadata, err := client.SecretMetadata(ctx, opts.Link.ID)
	if err != nil {
		return OpenResult{}, err
	}

	var passphrase, accessProof string
	if metadata.NeedsPassphrase() {
		if opts.Passphrase == nil {
			return OpenResult{}, errors.New("this secret needs a passphrase and none can be read")
		}
		if passphrase, err = opts.Passphrase(); err != nil {
			return OpenResult{}, err
		}
		if accessProof, err = clientcrypto.DeriveAccessProof(passphrase, metadata.AccessKDF()); err != nil {
			return OpenResult{}, err
		}
	} else if opts.Link.Key == nil {
		return OpenResult{}, errors.New(
			"this secret is opened with the key in the link fragment (#key=...), " +
				"but the link given carries no fragment",
		)
	}

	// Reserved before the open, not after: a missing directory, a name already
	// taken, or a read-only volume must cost nothing. Discarded if the open
	// itself fails, so a refused open leaves no empty file behind.
	reserved, err := reserveDestination(opts, metadata.Kind)
	if err != nil {
		return OpenResult{}, err
	}
	defer reserved.close()

	opened, err := client.OpenSecret(ctx, opts.Link.ID, accessProof)
	if err != nil {
		reserved.discard()
		return OpenResult{}, err
	}

	key := opts.Link.Key
	if metadata.NeedsPassphrase() {
		// Re-derived from the KDF block the payload came back with, not from
		// the metadata probe: the two salts are independent, and only the
		// payload's block describes the encryption key.
		if key, err = clientcrypto.KeyFromPayload(opened.Payload(), passphrase); err != nil {
			reserved.discard()
			return OpenResult{}, err
		}
	}

	plaintext, err := clientcrypto.Decrypt(opened.Payload(), key)
	if err != nil {
		// Nothing was recovered, so the reserved path must not be left behind
		// as an empty file where the user asked for a secret.
		reserved.discard()
		return OpenResult{}, err
	}

	result := OpenResult{Kind: opened.Kind, Plaintext: plaintext, ContentType: opened.ContentType}
	// The name is read only when it decides where the payload lands: with
	// -output the caller already named the path, and the held handle ignores
	// the name entirely. Nor is a name that fails an error: encrypted_filename
	// is optional in contracts/openapi.yaml, the body decrypted, and the server
	// has destroyed its copy — losing the secret over a missing label is the
	// worse outcome. writePayload falls back to "flick-file" for an empty name.
	if opened.Kind == "file" && opts.OutputPath == "" {
		result.Filename, _ = clientcrypto.DecryptFilename(opened.EncryptedFilename, key)
	}

	// A text secret with no -output belongs on the caller's stdout.
	if opts.OutputPath == "" && opened.Kind != "file" {
		return result, nil
	}

	result.WrittenPath, err = reserved.write(opts, result.Filename, plaintext)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	return result, nil
}

// destination is an output path claimed before the secret is consumed. For an
// explicit -output it holds an open, exclusively created file; for a file
// secret written under its decrypted name it holds only a checked directory,
// since the name is not known until after decryption.
type destination struct {
	file *os.File
	path string
}

func (d *destination) close() {
	if d.file != nil {
		d.file.Close()
	}
}

// discard removes a reserved file that will never be written, so a refused open
// does not leave an empty file where the user asked for a secret.
func (d *destination) discard() {
	if d.file != nil {
		d.file.Close()
		d.file = nil
		os.Remove(d.path)
	}
}

func (d *destination) write(opts OpenOptions, filename string, payload []byte) (string, error) {
	if d.file != nil {
		if _, err := d.file.Write(payload); err != nil {
			return "", err
		}
		return d.path, d.file.Sync()
	}
	return writePayload(opts, filename, payload)
}

// reserveDestination claims the output before anything destructive happens.
func reserveDestination(opts OpenOptions, kind string) (*destination, error) {
	if opts.OutputPath != "" {
		// O_EXCL: an explicit -output is the caller's stated path, so a
		// collision is an error rather than a silent overwrite. Truncating here
		// would destroy a local file to make room for the secret.
		file, err := os.OpenFile(opts.OutputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("%s already exists", opts.OutputPath)
			}
			return nil, err
		}
		return &destination{file: file, path: opts.OutputPath}, nil
	}
	if kind != "file" {
		return &destination{}, nil
	}
	// The filename is only known after decryption, so the directory is what can
	// be checked in advance. Catching a typo here costs nothing; catching it
	// after the open would cost the secret.
	if err := checkWritableDir(opts.OutputDir); err != nil {
		return nil, err
	}
	return &destination{}, nil
}

func checkWritableDir(dir string) error {
	if dir == "" {
		dir = "."
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("output directory is not usable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	probe, err := os.CreateTemp(dir, ".flick-write-check-*")
	if err != nil {
		return fmt.Errorf("output directory is not writable: %w", err)
	}
	probe.Close()
	return os.Remove(probe.Name())
}

// writePayload writes a file secret under its decrypted name without ever
// overwriting an existing file. The name comes from the sender, so it is
// reduced to its base name first: a payload named "../../.ssh/authorized_keys"
// must land in the output directory as "authorized_keys", not escape it.
func writePayload(opts OpenOptions, filename string, payload []byte) (string, error) {
	target := filepath.Join(opts.OutputDir, safeFilename(filename))

	for attempt := range 100 {
		candidate := target
		if attempt > 0 {
			candidate = numberedPath(target, attempt)
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		defer file.Close()
		if _, err := file.Write(payload); err != nil {
			return "", err
		}
		return candidate, file.Sync()
	}
	return "", fmt.Errorf("could not find an unused filename near %s", target)
}

// safeFilename reduces a sender-supplied name to something that can only land
// inside the output directory.
func safeFilename(name string) string {
	// Windows-style separators survive filepath.Base on Unix, so they are
	// normalized first — a sender is not bound by the recipient's OS.
	cleaned := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if cleaned == "." || cleaned == ".." || cleaned == string(filepath.Separator) || cleaned == "" {
		return "flick-file"
	}
	return cleaned
}

func numberedPath(path string, n int) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(path, ext), n, ext)
}
