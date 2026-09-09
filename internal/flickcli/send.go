package flickcli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

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
	// FileBytes is the file payload. A non-empty FileName selects a file secret.
	FileBytes []byte
	// ContentType overrides the type guessed from the file extension.
	ContentType string
	// Passphrase selects Model A when non-empty: the key is derived from it and
	// the recipient must type it. Empty selects Model B, where a random key
	// travels in the link fragment.
	Passphrase string
	// TTL is the secret's lifetime.
	TTL time.Duration
	// ShareOrigin is the web origin used to render the returned link. Empty
	// falls back to the API base URL, which is the same host in a deployed
	// Flick (deploy/base/ingress.yaml).
	ShareOrigin string
}

// SendResult is what the caller prints. Link already carries the Model B key in
// its fragment when there is one.
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

// Send encrypts locally and creates the secret. The server receives ciphertext,
// a nonce, and metadata — never the passphrase, the key, or the plaintext.
func Send(ctx context.Context, client *Client, opts SendOptions) (SendResult, error) {
	if err := validateSendOptions(opts); err != nil {
		return SendResult{}, err
	}

	plaintext := []byte(opts.Text)
	isFile := opts.FileName != ""
	if isFile {
		plaintext = opts.FileBytes
	}

	key, kdf, access, err := deriveSendKey(opts.Passphrase)
	if err != nil {
		return SendResult{}, err
	}

	payload, err := clientcrypto.Encrypt(plaintext, key, kdf)
	if err != nil {
		return SendResult{}, fmt.Errorf("encrypt payload: %w", err)
	}

	req := CreateSecretRequest{
		Kind:       "text",
		Ciphertext: payload.Ciphertext,
		Nonce:      payload.Nonce,
		SizeBytes:  payload.SizeBytes,
		TTLSeconds: int(opts.TTL.Seconds()),
		MaxViews:   1,
	}
	if access != nil {
		req.KDF = &payload.KDF
		req.Access = access
	}

	limits := client.Config(ctx)
	large := false
	if isFile {
		req.Kind = "file"
		req.ContentType = resolveContentType(opts.FileName, opts.ContentType)
		req.EncryptedFilename, err = clientcrypto.EncryptFilename(opts.FileName, key)
		if err != nil {
			return SendResult{}, fmt.Errorf("encrypt filename: %w", err)
		}
		if int64(payload.SizeBytes) > limits.MaxFileBytes {
			return SendResult{}, fmt.Errorf(
				"file is %d bytes; this deployment accepts at most %d",
				payload.SizeBytes, limits.MaxFileBytes,
			)
		}
		// Above the inline threshold the ciphertext goes straight to object
		// storage, so it is omitted here — that omission is what asks the
		// server for a presigned upload.
		if int64(payload.SizeBytes) > limits.PayloadInlineMaxBytes {
			large = true
			req.Ciphertext = ""
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
		ciphertext, err := decodeCiphertext(payload.Ciphertext)
		if err != nil {
			return SendResult{}, err
		}
		if err := client.Upload(ctx, *created.Upload, ciphertext); err != nil {
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
	model := "link-key"
	if access != nil {
		model = "passphrase"
	}
	return SendResult{
		ID:        created.ID,
		ExpiresAt: created.ExpiresAt,
		Link:      BuildShareLink(origin, created.ID, linkKey(opts.Passphrase, key)),
		Model:     model,
		Uploaded:  large,
	}, nil
}

func validateSendOptions(opts SendOptions) error {
	switch {
	case opts.Text == "" && opts.FileName == "":
		return errors.New("nothing to send: provide text or a file")
	case opts.Text != "" && opts.FileName != "":
		return errors.New("send text or a file, not both")
	case opts.TTL < MinTTL:
		return fmt.Errorf("TTL %s is below the minimum %s", opts.TTL, MinTTL)
	case opts.TTL > MaxTTL:
		return fmt.Errorf("TTL %s is above the maximum %s", opts.TTL, MaxTTL)
	}
	return nil
}

// deriveSendKey returns the encryption key plus, for Model A, the KDF block and
// access verifier the server stores. Model B returns a zero KDF and no
// verifier, which the request then omits.
func deriveSendKey(passphrase string) ([]byte, clientcrypto.KDFParams, *clientcrypto.AccessVerifier, error) {
	if passphrase == "" {
		key, err := clientcrypto.NewRandomKey()
		if err != nil {
			return nil, clientcrypto.KDFParams{}, nil, err
		}
		return key, clientcrypto.KDFParams{}, nil, nil
	}

	key, kdf, err := clientcrypto.NewKDF(passphrase)
	if err != nil {
		return nil, clientcrypto.KDFParams{}, nil, err
	}
	verifier, err := clientcrypto.NewAccessVerifier(passphrase)
	if err != nil {
		return nil, clientcrypto.KDFParams{}, nil, err
	}
	return key, kdf, &verifier, nil
}

// linkKey keeps the Model A key out of the share link. A passphrase-derived
// key in the fragment would defeat the passphrase entirely.
func linkKey(passphrase string, key []byte) []byte {
	if passphrase != "" {
		return nil
	}
	return key
}

// decodeCiphertext converts the base64 payload back to bytes for the presigned
// PUT. The signed Content-Length covers the raw ciphertext, and base64 is a
// third longer, so uploading the encoded form fails authentication at the
// bucket rather than anywhere useful.
func decodeCiphertext(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("encode ciphertext for upload: %w", err)
	}
	return raw, nil
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
