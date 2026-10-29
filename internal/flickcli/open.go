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

// OpenResult reports what was recovered. A text secret's Plaintext is always
// populated, even when writing it out failed, so a caller can still salvage a
// payload the server has already destroyed. A file secret is streamed to disk
// and never held whole in memory, so it has no Plaintext.
type OpenResult struct {
	Kind        string
	Plaintext   []byte
	Filename    string
	ContentType string
	// WrittenPath is the file actually written, which may differ from the
	// requested name when a collision was avoided. Empty means nothing was
	// written and the caller owns the payload.
	WrittenPath string
	// RescuedPath is set with ErrWriteAfterConsume when a file secret was
	// fully decrypted to a staging file that could not be moved into place.
	// That file is the only remaining copy.
	RescuedPath string
	// FilenameUnreadable reports that the secret carried a name that did not
	// decrypt, as opposed to carrying none. The payload is written either way,
	// but the two must not look alike to the recipient: the only causes are
	// corruption and tampering, and that is worth a word on stderr.
	FilenameUnreadable bool
}

// ErrWriteAfterConsume marks the one unrecoverable-looking case: the secret was
// consumed on the server and then could not be written locally. Whatever
// survived is in OpenResult.Plaintext (text) or OpenResult.RescuedPath (a
// file), and the caller must surface it rather than discard it — the server
// has no second copy. A file whose write failed mid-stream has neither.
var ErrWriteAfterConsume = errors.New("secret was opened and consumed but could not be written")

// Open consumes the secret and decrypts it locally.
//
// Order matters and is not incidental. Everything that can fail is done before
// the consuming open: the metadata probe, the passphrase prompt, the check that
// a link-key secret actually carries its identity, and — because a secret destroyed
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

	// Reserved before the open, not after: a missing directory, a name already
	// taken, or a read-only volume must cost nothing. Discarded if the open
	// itself fails, so a refused open leaves no empty file behind.
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
		// The verified open returns the identity wrapped under the same
		// passphrase that produced the access proof.
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

	// The name is read only when it decides where the payload lands: with
	// -output the caller already named the path, and the held handle ignores
	// the name entirely. Nor is a name that fails an error: encrypted_filename
	// is optional in contracts/openapi.yaml, the body is separately
	// authenticated, and the server has destroyed its copy — losing the secret
	// over a missing label is the worse outcome. placeFile falls back to
	// "flick-file" for an empty name.
	if opts.OutputPath == "" && opened.EncryptedFilename != "" {
		name, err := openFilename(identity, opened.EncryptedFilename)
		result.Filename = name
		result.FilenameUnreadable = err != nil
	}

	// The file is decrypted into a staging file beside its destination and
	// moved into place only after age authenticates the final chunk, so a
	// truncated or altered body never leaves partial plaintext where the user
	// asked for the secret.
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
		return result, err
	}
	if result.WrittenPath, err = reserved.place(opts, result.Filename, staged); err != nil {
		result.RescuedPath = staged.Name()
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	return result, nil
}

// openText decrypts a text secret in memory: it is bounded by the inline limit
// and goes to stdout or a small file, and a failed write can still rescue it.
func openText(opts OpenOptions, reserved *destination, identity *age.HybridIdentity, opened OpenedSecret, result OpenResult) (OpenResult, error) {
	// unseal returns only after age authenticates the final chunk, so a
	// truncated or altered body yields an error, never partial output.
	plaintext, err := unsealReader(identity, opened.Body)
	if err != nil {
		// Nothing was recovered, so the reserved path must not be left behind
		// as an empty file where the user asked for a secret.
		reserved.discard()
		return OpenResult{}, err
	}
	result.Plaintext = plaintext
	// A text secret with no -output belongs on the caller's stdout.
	if opts.OutputPath == "" {
		return result, nil
	}
	if err := reserved.writeText(plaintext); err != nil {
		return result, fmt.Errorf("%w: %w", ErrWriteAfterConsume, err)
	}
	result.WrittenPath = opts.OutputPath
	return result, nil
}

// stagingWriteError marks a local write failure while decrypting into the
// staging file, as opposed to the body failing to decrypt.
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

// decryptInto streams body through age into staged and syncs it.
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

// unseal decrypts an age file encrypted to identity.
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

// openFilename decrypts the base64 age file carrying a file secret's name.
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

func (d *destination) writeText(payload []byte) error {
	if _, err := d.file.Write(payload); err != nil {
		return err
	}
	return d.file.Sync()
}

// stage creates the staging file in the directory the payload will land in,
// so moving it into place is a rename or link on the same file system.
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

// place moves a fully decrypted staging file to its destination. An explicit
// -output replaces the empty file reserved for it; otherwise the decrypted
// name is used without ever overwriting an existing file.
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

// placeFile links the staged file to its decrypted name without ever
// overwriting an existing file, then removes the staging name. The name comes
// from the sender, so it is reduced to its base name first: a payload named
// "../../.ssh/authorized_keys" must land in the output directory as
// "authorized_keys", not escape it.
func placeFile(opts OpenOptions, filename, staged string) (string, error) {
	target := filepath.Join(opts.OutputDir, safeFilename(filename))

	for attempt := range 100 {
		candidate := target
		if attempt > 0 {
			candidate = numberedPath(target, attempt)
		}
		// A hard link fails if the name exists, which is the no-overwrite
		// guarantee O_EXCL gave the old write path.
		err := os.Link(staged, candidate)
		if err != nil && !errors.Is(err, os.ErrExist) {
			// Some file systems (FAT, exFAT, some network mounts) have no
			// hard links; copy into an exclusively created file instead.
			err = copyExclusive(staged, candidate)
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		os.Remove(staged)
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
