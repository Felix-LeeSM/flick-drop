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
	// OutputPath writes the payload to this exact path. Empty means text goes
	// to the caller's stdout and a file is written under its decrypted name in
	// OutputDir.
	OutputPath string
	// OutputDir is where a file secret lands when OutputPath is empty.
	OutputDir string
}

// OpenResult reports what was recovered. Text secrets carry Plaintext; file
// secrets carry Filename and WrittenPath.
type OpenResult struct {
	Kind        string
	Plaintext   []byte
	Filename    string
	ContentType string
	// WrittenPath is the file actually written, which may differ from the
	// requested name when a collision was avoided.
	WrittenPath string
}

// Open consumes the secret and decrypts it locally.
//
// Order matters and is not incidental: the metadata probe and the passphrase
// prompt both happen before the consuming open, so an abandoned prompt or a
// missing key costs nothing. Once OpenSecret returns, the payload exists only
// in this process — a failure after that point loses the secret for good, which
// is why the result is decrypted and returned rather than streamed straight to
// a file handle that might not open.
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

	opened, err := client.OpenSecret(ctx, opts.Link.ID, accessProof)
	if err != nil {
		return OpenResult{}, err
	}

	key := opts.Link.Key
	if metadata.NeedsPassphrase() {
		// Re-derived from the KDF block the payload came back with, not from
		// the metadata probe: the two salts are independent, and only the
		// payload's block describes the encryption key.
		if key, err = clientcrypto.KeyFromPayload(opened.Payload(), passphrase); err != nil {
			return OpenResult{}, err
		}
	}

	plaintext, err := clientcrypto.Decrypt(opened.Payload(), key)
	if err != nil {
		return OpenResult{}, err
	}

	result := OpenResult{Kind: opened.Kind, Plaintext: plaintext, ContentType: opened.ContentType}
	if opened.Kind != "file" {
		return result, nil
	}

	if result.Filename, err = clientcrypto.DecryptFilename(opened.EncryptedFilename, key); err != nil {
		return result, fmt.Errorf("decrypt filename: %w", err)
	}
	if result.WrittenPath, err = writePayload(opts, result.Filename, plaintext); err != nil {
		// The secret is already consumed at this point, so the error names the
		// payload as still recoverable from the process output rather than
		// implying it can be fetched again.
		return result, fmt.Errorf("secret was opened and consumed but could not be written: %w", err)
	}
	return result, nil
}

// writePayload writes a file secret without ever overwriting an existing file.
// The filename comes from the sender, so it is reduced to its base name first:
// a payload named "../../.ssh/authorized_keys" must land in the output
// directory as "authorized_keys", not escape it.
func writePayload(opts OpenOptions, filename string, payload []byte) (string, error) {
	target := opts.OutputPath
	if target == "" {
		target = filepath.Join(opts.OutputDir, safeFilename(filename))
	}

	for attempt := range 100 {
		candidate := target
		if attempt > 0 {
			candidate = numberedPath(target, attempt)
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			// An explicit --output is the caller's stated choice of path, so a
			// collision there is an error rather than a silent rename.
			if opts.OutputPath != "" {
				return "", fmt.Errorf("%s already exists", candidate)
			}
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
