package flickcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Called only for Model A secrets, so a link-key secret never prompts.
type PassphraseFunc func() (string, error)

type OpenOptions struct {
	Link ShareLink
	// Called after the metadata probe and before the consuming open.
	Passphrase PassphraseFunc
	// Empty: text goes to stdout and a file lands under its decrypted name in OutputDir.
	OutputPath string
	OutputDir  string
}

// Plaintext is set for text even when the write failed, for salvage; file secrets stream to disk and have none.
type OpenResult struct {
	Kind        string
	Plaintext   []byte
	Filename    string
	ContentType string
	// May differ from the requested name after a collision. Empty: nothing written, the caller owns the payload.
	WrittenPath string
	// Set with ErrWriteAfterConsume: the staging file is the only remaining copy.
	RescuedPath string
	// A name that failed to decrypt (corruption or tampering), as opposed to no name.
	FilenameUnreadable bool
}

// The server has no second copy: callers must surface OpenResult.Plaintext or RescuedPath.
var ErrWriteAfterConsume = errors.New("secret was opened and consumed but could not be written")

// Order matters: everything that can fail (probe, passphrase, identity check, reserving the
// output) runs before the consuming open; afterwards the payload exists only in this process.
func Open(ctx context.Context, client *Client, opts OpenOptions) (OpenResult, error) {
	if opts.Link.ID == "" {
		return OpenResult{}, errors.New("no secret ID to open")
	}

	metadata, err := client.SecretMetadata(ctx, opts.Link.ID)
	if err != nil {
		return OpenResult{}, err
	}

	identity := opts.Link.Identity
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
	} else if identity == nil {
		return OpenResult{}, errors.New(
			"this secret is opened with the key in the link fragment (#key=...), " +
				"but the link given carries no fragment",
		)
	}

	// The server-reported size is the only bound on an in-memory text decrypt, so check before consuming.
	if metadata.Kind != "file" {
		if limit := ageFileBytes(client.Config(ctx).PayloadInlineMaxBytes); int64(metadata.SizeBytes) > limit {
			return OpenResult{}, fmt.Errorf("text secret reports %d bytes, more than the %d this deployment stores inline", metadata.SizeBytes, limit)
		}
	}

	// Reserved before the open so a bad path costs nothing; discarded if the open fails.
	reserved, err := reserveDestination(opts, metadata.Kind)
	if err != nil {
		return OpenResult{}, err
	}
	defer reserved.close()

	opened, err := client.OpenSecret(ctx, opts.Link.ID, accessProof, metadata.SizeBytes)
	if err != nil {
		reserved.discard()
		return OpenResult{}, err
	}
	defer opened.Body.Close()

	if metadata.NeedsPassphrase() {
		if opened.WrappedIdentity == nil {
			reserved.discard()
			return OpenResult{}, errors.New("open response carries no wrapped identity for a passphrase secret")
		}
		if identity, err = clientcrypto.UnwrapIdentity(opened.WrappedIdentity, passphrase); err != nil {
			reserved.discard()
			return OpenResult{}, err
		}
	}

	result := OpenResult{Kind: opened.Kind, ContentType: opened.ContentType}
	if opened.Kind != "file" {
		return openText(opts, reserved, identity, opened, result)
	}

	// A name that fails to decrypt is not an error: encrypted_filename is optional, the body is
	// separately authenticated, and the server copy is already gone.
	if opts.OutputPath == "" && opened.EncryptedFilename != "" {
		name, err := openFilename(identity, opened.EncryptedFilename)
		result.Filename = name
		result.FilenameUnreadable = err != nil
	}

	// Staged and moved into place only after age authenticates the final chunk, so no partial plaintext lands.
	staged, err := reserved.stage(opts)
	if err != nil {
		reserved.discard()
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	if err := decryptInto(staged, identity, opened.Body); err != nil {
		staged.Close()
		os.Remove(staged.Name())
		reserved.discard()
		var writeErr *stagingWriteError
		if errors.As(err, &writeErr) {
			return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, writeErr.err)
		}
		return result, consumedError(err)
	}
	if result.WrittenPath, err = reserved.place(opts, result.Filename, staged); err != nil {
		if result.WrittenPath != "" {
			return result, err
		}
		result.RescuedPath = staged.Name()
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	return result, nil
}

func openText(opts OpenOptions, reserved *destination, identity *age.HybridIdentity, opened OpenedSecret, result OpenResult) (OpenResult, error) {
	plaintext, err := unsealReader(identity, opened.Body)
	if err != nil {
		reserved.discard()
		return OpenResult{}, consumedError(err)
	}
	result.Plaintext = plaintext
	if opts.OutputPath == "" {
		return result, nil
	}
	if err := reserved.writeText(plaintext); err != nil {
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	result.WrittenPath = opts.OutputPath
	return result, nil
}

// Tells the user not to retry a link that is already gone.
func consumedError(err error) error {
	return fmt.Errorf("the secret was opened and is gone from the server, but its contents could not be read: %w", err)
}

// Twin of internal/secrets.MaxCiphertextBytes (contracts/age-format-v1.md), which this client must not import.
func ageFileBytes(n int64) int64 {
	return 4096 + 16 + n + 16*max(1, (n+65535)/65536)
}

type stagingWriteError struct{ err error }

func (e *stagingWriteError) Error() string { return e.err.Error() }

type stagingWriter struct{ f *os.File }

func (w stagingWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, &stagingWriteError{err}
	}
	return n, nil
}

func decryptInto(staged *os.File, identity *age.HybridIdentity, body io.Reader) error {
	r, err := clientcrypto.DecryptWith(body, identity)
	if err != nil {
		return err
	}
	if _, err := io.Copy(stagingWriter{staged}, r); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return &stagingWriteError{err}
	}
	return nil
}

func unseal(identity *age.HybridIdentity, file []byte) ([]byte, error) {
	return unsealReader(identity, bytes.NewReader(file))
}

func unsealReader(identity *age.HybridIdentity, src io.Reader) ([]byte, error) {
	r, err := clientcrypto.DecryptWith(src, identity)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func openFilename(identity *age.HybridIdentity, encoded string) (string, error) {
	file, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	name, err := unseal(identity, file)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(name) {
		return "", errors.New("filename is not UTF-8")
	}
	return string(name), nil
}

// Explicit -output holds an exclusively created file; otherwise only a checked directory, since the name comes after decryption.
type destination struct {
	file *os.File
	path string
}

func (d *destination) close() {
	if d.file != nil {
		d.file.Close()
	}
}

func (d *destination) discard() {
	if d.file != nil {
		d.file.Close()
		d.file = nil
		os.Remove(d.path)
	}
}

func (d *destination) writeText(payload []byte) error {
	if _, err := d.file.Write(payload); err != nil {
		return err
	}
	return d.file.Sync()
}

// Same directory as the destination, so placing is a rename or link on one file system.
func (d *destination) stage(opts OpenOptions) (*os.File, error) {
	dir := opts.OutputDir
	if d.file != nil {
		dir = filepath.Dir(d.path)
	}
	if dir == "" {
		dir = "."
	}
	return os.CreateTemp(dir, ".flick-partial-*")
}

func (d *destination) place(opts OpenOptions, filename string, staged *os.File) (string, error) {
	if err := staged.Close(); err != nil {
		return "", err
	}
	if d.file != nil {
		d.file.Close()
		d.file = nil
		if err := os.Rename(staged.Name(), d.path); err != nil {
			return "", err
		}
		return d.path, nil
	}
	return placeFile(opts, filename, staged.Name())
}

func reserveDestination(opts OpenOptions, kind string) (*destination, error) {
	if opts.OutputPath != "" {
		// O_EXCL: never truncate a local file to make room for the secret.
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
	// Catching a bad directory after the open would cost the secret.
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

// The name comes from the sender: reduced to its base name so "../../.ssh/authorized_keys" cannot escape.
func placeFile(opts OpenOptions, filename, staged string) (string, error) {
	target := filepath.Join(opts.OutputDir, safeFilename(filename))

	for attempt := range 100 {
		candidate := target
		if attempt > 0 {
			candidate = numberedPath(target, attempt)
		}
		// A hard link fails if the name exists: the no-overwrite guarantee.
		err := os.Link(staged, candidate)
		if err != nil && !errors.Is(err, os.ErrExist) {
			// FAT, exFAT, and some network mounts have no hard links.
			err = copyExclusive(staged, candidate)
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := os.Remove(staged); err != nil {
			return candidate, fmt.Errorf("saved %s, but could not remove the staging link %s: %w", candidate, staged, err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("could not find an unused filename near %s", target)
}

func copyExclusive(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func safeFilename(name string) string {
	// Backslashes survive filepath.Base on Unix; a sender is not bound by the recipient's OS.
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
