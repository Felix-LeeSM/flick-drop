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

// Match contracts/openapi.yaml ttl_seconds and the web image's PUBLIC_FLICK_*_TTL_SECONDS.
const (
	MinTTL     = 5 * time.Minute
	MaxTTL     = 7 * 24 * time.Hour
	DefaultTTL = time.Hour
)

// Exactly one of Text or FileName carries the payload.
type SendOptions struct {
	Text string
	// Encrypted alongside the body, never sent in the clear.
	FileName string
	// Read once and encrypted as read; never held whole in memory.
	File        io.Reader
	ContentType string
	// Non-empty selects Model A; empty selects Model B (identity in the link fragment).
	Passphrase string
	TTL        time.Duration
	// Empty falls back to the API base URL (same host when deployed, deploy/base/ingress.yaml).
	ShareOrigin string
}

type SendResult struct {
	ID        string
	ExpiresAt string
	Link      string
	// "passphrase" (Model A) or "link-key" (Model B).
	Model    string
	Uploaded bool
}

// The server receives age files and metadata, never the passphrase, the clear identity, or the plaintext.
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
		// Config limits count plaintext bytes; the server's caps include age overhead.
		sealed, err = sealFile(identity, opts.File, limits.MaxFileBytes)
		if err != nil {
			return SendResult{}, err
		}
		defer sealed.remove()
	} else {
		if limits.PayloadInlineMaxBytes > 0 && int64(len(opts.Text)) > limits.PayloadInlineMaxBytes {
			return SendResult{}, fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(opts.Text), limits.PayloadInlineMaxBytes)
		}
		if body, err = seal(identity, []byte(opts.Text)); err != nil {
			return SendResult{}, fmt.Errorf("encrypt payload: %w", err)
		}
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
		// Omitting the body asks for a presigned upload, signed at exactly sealed.size, so raw bytes are uploaded.
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
	// The identity never goes in a Model A link: it would defeat the passphrase.
	if opts.Passphrase != "" {
		result.Model = "passphrase"
		result.Link = BuildShareLink(origin, created.ID, nil)
	} else {
		result.Link = BuildShareLink(origin, created.ID, identity)
	}
	return result, nil
}

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

// Holds only ciphertext.
type sealedFile struct {
	file           *os.File
	size           int64
	plaintextBytes int64
}

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
