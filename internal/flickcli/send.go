package flickcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// TTL bounds match contracts/openapi.yaml CreateSecretRequest.ttl_seconds and
// the PUBLIC_FLICK_*_TTL_SECONDS values the web image is built with.
const (
	MinTTL     = 5 * time.Minute
	MaxTTL     = 7 * 24 * time.Hour
	DefaultTTL = time.Hour
)

// SendOptions describes one secret to create. Exactly one of Text or FileName
// carries the payload.
type SendOptions struct {
	// Text is the plaintext for a text secret.
	Text string
	// FileName is the original filename for a file secret; it is encrypted
	// alongside the body and never sent in the clear.
	FileName string
	// File is read once and encrypted as it is read, so the CLI never holds a
	// whole file in memory. A non-empty FileName selects a file secret.
	File io.Reader
	// ContentType overrides the type guessed from the file extension.
	ContentType string
	// Passphrase selects Model A when non-empty: it wraps the secret's age
	// identity and the recipient must type it. Empty selects Model B, where the
	// identity travels in the link fragment.
	Passphrase string
	// TTL is the secret's lifetime.
	TTL time.Duration
	// ShareOrigin is the web origin used to render the returned link. Empty
	// falls back to the API base URL, which is the same host in a deployed
	// Flick (deploy/base/ingress.yaml).
	ShareOrigin string
}

// SendResult is what the caller prints. Link already carries the Model B
// identity in its fragment when there is one.
type SendResult struct {
	ID        string
	ExpiresAt string
	Link      string
	// Model is "passphrase" (Model A) or "link-key" (Model B).
	Model string
	// Uploaded reports whether the ciphertext went to object storage through a
	// presigned PUT rather than inline in the create request.
	Uploaded bool
}

// Send encrypts locally and creates the secret. Every secret gets a fresh age
// identity (contracts/age-format-v1.md). The server receives age files and
// metadata — never the passphrase, the identity in the clear, or the plaintext.
func Send(ctx context.Context, client *Client, opts SendOptions) (SendResult, error) {
	if err := validateSendOptions(opts); err != nil {
		return SendResult{}, err
	}

	limits := client.Config(ctx)
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		return SendResult{}, err
	}

	isFile := opts.FileName != ""
	var body []byte
	var sealed *sealedFile
	if isFile {
		// Limits from /api/config count bytes before encryption; the server's
		// caps on the age file already include the age overhead.
		sealed, err = sealFile(identity, opts.File, limits.MaxFileBytes)
		if err != nil {
			return SendResult{}, err
		}
		defer sealed.remove()
	} else if body, err = seal(identity, []byte(opts.Text)); err != nil {
		return SendResult{}, fmt.Errorf("encrypt payload: %w", err)
	}

	req := CreateSecretRequest{
		Kind:       "text",
		Ciphertext: base64.StdEncoding.EncodeToString(body),
		SizeBytes:  len(body),
		TTLSeconds: int(opts.TTL.Seconds()),
		MaxViews:   1,
	}
	if opts.Passphrase != "" {
		if err := addPassphrase(&req, identity, opts.Passphrase); err != nil {
			return SendResult{}, err
		}
	}

	large := false
	if isFile {
		req.Kind = "file"
		req.ContentType = resolveContentType(opts.FileName, opts.ContentType)
		filename, err := seal(identity, []byte(opts.FileName))
		if err != nil {
			return SendResult{}, fmt.Errorf("encrypt filename: %w", err)
		}
		req.EncryptedFilename = base64.StdEncoding.EncodeToString(filename)
		req.SizeBytes = int(sealed.size)
		// Above the inline threshold the body goes straight to object
		// storage, so it is omitted here — that omission is what asks the
		// server for a presigned upload. The presigned PUT is signed at
		// exactly sealed.size, so the raw bytes are uploaded, not base64.
		if sealed.plaintextBytes > limits.PayloadInlineMaxBytes {
			large = true
			req.Ciphertext = ""
		} else {
			if body, err = sealed.bytes(); err != nil {
				return SendResult{}, err
			}
			req.Ciphertext = base64.StdEncoding.EncodeToString(body)
		}
	}

	created, err := client.CreateSecret(ctx, req)
	if err != nil {
		return SendResult{}, err
	}

	if large {
		if created.Upload == nil {
			return SendResult{}, errors.New(
				"server did not return a presigned upload for a large file; " +
					"this deployment may not have object storage enabled",
			)
		}
		if err := client.Upload(ctx, *created.Upload, sealed.file, sealed.size); err != nil {
			return SendResult{}, err
		}
		// Until finalize succeeds the secret stays pending and cannot be
		// opened, so a failure here is worth surfacing rather than returning a
		// link that will not work.
		if err := client.FinalizeSecret(ctx, created.ID); err != nil {
			return SendResult{}, err
		}
	}

	origin := opts.ShareOrigin
	if origin == "" {
		origin = client.BaseURL()
	}
	result := SendResult{
		ID:        created.ID,
		ExpiresAt: created.ExpiresAt,
		Model:     "link-key",
		Uploaded:  large,
	}
	// Model A keeps the identity out of the link: in the fragment it would
	// defeat the passphrase entirely.
	if opts.Passphrase != "" {
		result.Model = "passphrase"
		result.Link = BuildShareLink(origin, created.ID, nil)
	} else {
		result.Link = BuildShareLink(origin, created.ID, identity)
	}
	return result, nil
}

// addPassphrase sets the Model A fields: the PBKDF2 access proof that gates the
// open, and the identity wrapped in an age scrypt file under the same
// passphrase.
func addPassphrase(req *CreateSecretRequest, identity *age.HybridIdentity, passphrase string) error {
	verifier, err := clientcrypto.NewAccessVerifier(passphrase)
	if err != nil {
		return err
	}
	wrapped, err := clientcrypto.WrapIdentity(identity, passphrase)
	if err != nil {
		return fmt.Errorf("wrap identity: %w", err)
	}
	req.Access = &verifier
	req.WrappedIdentity = base64.StdEncoding.EncodeToString(wrapped)
	return nil
}

// sealedFile is a file secret's age file, staged in a temporary file so a large
// upload streams from disk. It holds only ciphertext.
type sealedFile struct {
	file           *os.File
	size           int64
	plaintextBytes int64
}

// sealFile encrypts src to identity, 64 KiB at a time, and refuses more than
// limit bytes of input.
func sealFile(identity *age.HybridIdentity, src io.Reader, limit int64) (*sealedFile, error) {
	staged, err := os.CreateTemp("", "flick-send-*.age")
	if err != nil {
		return nil, fmt.Errorf("stage encrypted file: %w", err)
	}
	sealed := &sealedFile{file: staged}
	if err := sealed.fill(identity, src, limit); err != nil {
		sealed.remove()
		return nil, err
	}
	return sealed, nil
}

func (s *sealedFile) fill(identity *age.HybridIdentity, src io.Reader, limit int64) error {
	w, err := clientcrypto.EncryptTo(s.file, identity)
	if err != nil {
		return err
	}
	if s.plaintextBytes, err = io.Copy(w, io.LimitReader(src, limit+1)); err != nil {
		return fmt.Errorf("encrypt file: %w", err)
	}
	if s.plaintextBytes > limit {
		return fmt.Errorf("file is larger than %d bytes, the most this deployment accepts", limit)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("encrypt file: %w", err)
	}
	if s.size, err = s.file.Seek(0, io.SeekCurrent); err != nil {
		return err
	}
	_, err = s.file.Seek(0, io.SeekStart)
	return err
}

// bytes reads the staged age file for an inline create, which only happens
// below the inline limit.
func (s *sealedFile) bytes() ([]byte, error) {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(s.file)
}

func (s *sealedFile) remove() {
	s.file.Close()
	os.Remove(s.file.Name())
}

// seal returns plaintext as an age file encrypted to identity.
func seal(identity *age.HybridIdentity, plaintext []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := clientcrypto.EncryptTo(&out, identity)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func validateSendOptions(opts SendOptions) error {
	switch {
	case opts.Text == "" && opts.FileName == "":
		return errors.New("nothing to send: provide text or a file")
	case opts.Text != "" && opts.FileName != "":
		return errors.New("send text or a file, not both")
	case opts.FileName != "" && opts.File == nil:
		return errors.New("no file contents to send")
	case opts.TTL < MinTTL:
		return fmt.Errorf("TTL %s is below the minimum %s", opts.TTL, MinTTL)
	case opts.TTL > MaxTTL:
		return fmt.Errorf("TTL %s is above the maximum %s", opts.TTL, MaxTTL)
	}
	return nil
}

func resolveContentType(filename, override string) string {
	if override != "" {
		return override
	}
	if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}
