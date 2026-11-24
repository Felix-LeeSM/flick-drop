package flickcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Request-link flows, peers of the web's CreateRequestPage, SubmitRequestPage,
// and ReceiveRequestPage (docs/architecture/request-links.md). The requester
// keeps the identity in the retrieval link; the submitter only ever sees the
// recipient string.

// The requester note fits the server's 4,096-byte encrypted_note cap alongside
// the PQ header (web/src/lib/crypto/requests.ts REQUEST_NOTE_MAX_BYTES).
const RequestNoteMaxBytes = 1024

type RequestOptions struct {
	TTL         time.Duration
	Note        string
	ShareOrigin string
}

type RequestResult struct {
	ID             string
	ExpiresAt      string
	SubmissionLink string
	RetrievalLink  string
	HasNote        bool
}

func CreateRequest(ctx context.Context, client *Client, opts RequestOptions) (RequestResult, error) {
	switch {
	case opts.TTL < MinTTL:
		return RequestResult{}, fmt.Errorf("TTL %s is below the minimum %s", opts.TTL, MinTTL)
	case opts.TTL > MaxTTL:
		return RequestResult{}, fmt.Errorf("TTL %s is above the maximum %s", opts.TTL, MaxTTL)
	}

	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		return RequestResult{}, err
	}
	recipient := clientcrypto.RecipientStringOf(identity)

	input := CreateRequestInput{
		PublicKey:  recipient,
		TTLSeconds: int(opts.TTL.Seconds()),
	}
	var memo *age.HybridIdentity
	if opts.Note != "" {
		if len(opts.Note) > RequestNoteMaxBytes {
			return RequestResult{}, fmt.Errorf("the note is %d bytes; the most a request carries is %d", len(opts.Note), RequestNoteMaxBytes)
		}
		if memo, err = clientcrypto.NewIdentity(); err != nil {
			return RequestResult{}, err
		}
		sealed, err := sealToRecipient(clientcrypto.RecipientStringOf(memo), []byte(opts.Note))
		if err != nil {
			return RequestResult{}, err
		}
		input.EncryptedNote = base64.StdEncoding.EncodeToString(sealed)
	}

	created, err := client.CreateRequest(ctx, input)
	if err != nil {
		return RequestResult{}, err
	}
	return requestResult(opts.ShareOrigin, client, created, identity, recipient, memo), nil
}

func requestResult(shareOrigin string, client *Client, created CreatedRequest, identity *age.HybridIdentity, recipient string, memo *age.HybridIdentity) RequestResult {
	origin := shareOrigin
	if origin == "" {
		origin = client.BaseURL()
	}
	return RequestResult{
		ID:             created.ID,
		ExpiresAt:      created.ExpiresAt,
		SubmissionLink: BuildSubmissionLink(origin, created.ID, created.SubmissionToken, clientcrypto.RecipientFingerprint(recipient), memo),
		RetrievalLink:  BuildRetrievalLink(origin, created.ID, created.RetrievalToken, identity),
		HasNote:        memo != nil,
	}
}

type SubmitOptions struct {
	Link       SubmissionLink
	Text       string
	Credential string
	FileName   string
	File       io.Reader
}

type SubmitResult struct {
	Generation int
	State      string
	Note       string
	Uploaded   bool
}

// The ciphertext is frozen with the attempt token, so a network failure after
// the server decided resolves by asking about that exact attempt, never by
// re-encrypting (docs/architecture/request-links.md retry model).
func SubmitToRequest(ctx context.Context, client *Client, opts SubmitOptions) (SubmitResult, error) {
	instructions, err := client.RequestInstructions(ctx, opts.Link.ID, opts.Link.Token)
	if err != nil {
		return SubmitResult{}, err
	}
	if _, err := clientcrypto.ParseRecipient(instructions.PublicKey); err != nil {
		return SubmitResult{}, fmt.Errorf("the server's request key is invalid: %w", err)
	}
	if got := clientcrypto.RecipientFingerprint(instructions.PublicKey); got != opts.Link.Fingerprint {
		return SubmitResult{}, errors.New(
			"the server's request key does not match the fingerprint pinned in the submission link; " +
				"refusing to encrypt to a substituted key",
		)
	}

	result := SubmitResult{Generation: instructions.Generation}
	if opts.Link.MemoIdentity != nil && instructions.EncryptedNote != "" {
		if result.Note, err = openNote(opts.Link.MemoIdentity, instructions.EncryptedNote); err != nil {
			return SubmitResult{}, err
		}
	}
	if !instructions.CanSubmit {
		return result, fmt.Errorf("this request is not accepting a submission right now (generation %d); ask for a fresh link", instructions.Generation)
	}

	if opts.Credential != "" {
		cred, err := ParseCredentialInput([]byte(opts.Credential))
		if err != nil {
			return SubmitResult{}, err
		}
		opts.Text = SerializeCredential(cred)
		opts.Credential = ""
	}

	limits := client.Config(ctx)
	recipient := instructions.PublicKey
	attempt, err := clientcrypto.NewAttemptToken()
	if err != nil {
		return SubmitResult{}, err
	}
	attemptBody := RequestAttempt{Generation: instructions.Generation, AttemptToken: attempt}

	if opts.FileName != "" {
		return submitFile(ctx, client, opts, recipient, limits, attemptBody, result)
	}
	return submitText(ctx, client, opts.Link, opts.Text, recipient, limits, attemptBody, result)
}

func submitText(ctx context.Context, client *Client, link SubmissionLink, text, recipient string, limits Limits, attempt RequestAttempt, result SubmitResult) (SubmitResult, error) {
	if limits.PayloadInlineMaxBytes > 0 && int64(len(text)) > limits.PayloadInlineMaxBytes {
		return SubmitResult{}, fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(text), limits.PayloadInlineMaxBytes)
	}
	body, err := sealToRecipient(recipient, []byte(text))
	if err != nil {
		return SubmitResult{}, fmt.Errorf("encrypt payload: %w", err)
	}
	input := SubmitRequestInput{
		Generation:   attempt.Generation,
		AttemptToken: attempt.AttemptToken,
		Kind:         "text",
		SizeBytes:    int64(len(body)),
		Envelope:     RequestEnvelope{Version: 2},
		Ciphertext:   base64.StdEncoding.EncodeToString(body),
	}
	receipt, err := client.SubmitRequest(ctx, link.ID, link.Token, input)
	if err != nil {
		return SubmitResult{}, resolveAttempt(ctx, client, link, attempt, err)
	}
	result.Generation = receipt.Generation
	result.State = receipt.State
	return result, nil
}

func submitFile(ctx context.Context, client *Client, opts SubmitOptions, recipient string, limits Limits, attempt RequestAttempt, result SubmitResult) (SubmitResult, error) {
	name, err := sealToRecipient(recipient, []byte(opts.FileName))
	if err != nil {
		return SubmitResult{}, fmt.Errorf("encrypt filename: %w", err)
	}
	sealed, err := sealFileToRecipient(recipient, opts.File, limits.MaxFileBytes)
	if err != nil {
		return SubmitResult{}, err
	}
	defer sealed.remove()

	envelope := RequestEnvelope{Version: 2, EncryptedFilename: base64.StdEncoding.EncodeToString(name)}
	if sealed.plaintextBytes <= limits.PayloadInlineMaxBytes {
		body, err := sealed.bytes()
		if err != nil {
			return SubmitResult{}, err
		}
		input := SubmitRequestInput{
			Generation:   attempt.Generation,
			AttemptToken: attempt.AttemptToken,
			Kind:         "file",
			SizeBytes:    sealed.size,
			Envelope:     envelope,
			Ciphertext:   base64.StdEncoding.EncodeToString(body),
		}
		receipt, err := client.SubmitRequest(ctx, opts.Link.ID, opts.Link.Token, input)
		if err != nil {
			return SubmitResult{}, resolveAttempt(ctx, client, opts.Link, attempt, err)
		}
		result.Generation = receipt.Generation
		result.State = receipt.State
		return result, nil
	}

	if _, err := sealed.file.Seek(0, io.SeekStart); err != nil {
		return SubmitResult{}, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, sealed.file); err != nil {
		return SubmitResult{}, err
	}
	if _, err := sealed.file.Seek(0, io.SeekStart); err != nil {
		return SubmitResult{}, err
	}
	reserved, err := client.ReserveRequestUpload(ctx, opts.Link.ID, opts.Link.Token, RequestUploadInput{
		Generation:       attempt.Generation,
		AttemptToken:     attempt.AttemptToken,
		Kind:             "file",
		SizeBytes:        sealed.size,
		Envelope:         envelope,
		CiphertextSHA256: base64.StdEncoding.EncodeToString(hasher.Sum(nil)),
	})
	if err != nil {
		return SubmitResult{}, resolveAttempt(ctx, client, opts.Link, attempt, err)
	}
	if reserved.State == "submitted" {
		result.Generation = reserved.Generation
		result.State = reserved.State
		return result, nil
	}
	if reserved.Upload == nil {
		return SubmitResult{}, errors.New("server did not return a presigned upload for a large file; this deployment may not have object storage enabled")
	}
	if err := client.Upload(ctx, *reserved.Upload, sealed.file, sealed.size); err != nil {
		return SubmitResult{}, err
	}
	receipt, err := client.FinalizeRequest(ctx, opts.Link.ID, opts.Link.Token, attempt)
	if err != nil {
		return SubmitResult{}, resolveAttempt(ctx, client, opts.Link, attempt, err)
	}
	result.Generation = receipt.Generation
	result.State = receipt.State
	result.Uploaded = true
	return result, nil
}

// A server answer (an APIError) is a definite refusal; anything else means the
// outcome is unknown, and only the frozen attempt can settle it.
func resolveAttempt(ctx context.Context, client *Client, link SubmissionLink, attempt RequestAttempt, cause error) error {
	var apiErr *APIError
	if errors.As(cause, &apiErr) {
		return cause
	}
	status, err := client.InspectRequestAttempt(ctx, link.ID, link.Token, attempt)
	if err == nil && status.State == "accepted" {
		return fmt.Errorf("%w (the submission was accepted: generation %d)", cause, status.Generation)
	}
	return fmt.Errorf("%w (the attempt status is unknown; re-running submits a new attempt)", cause)
}

type ReceiveOptions struct {
	Link       RetrievalLink
	OutputPath string
	OutputDir  string
	Reveal     bool
	StatusOnly bool
	Revoke     bool
}

type ReceiveResult struct {
	State     string
	ExpiresAt string
	Opened    *OpenResult
}

// The retrieval open is as destructive as a secret open: the destination is
// reserved before the consuming call, and a failure after it leaves the reply
// only in this process.
func ReceiveRequest(ctx context.Context, client *Client, opts ReceiveOptions) (ReceiveResult, error) {
	owner, err := client.RequestOwnerStatus(ctx, opts.Link.ID, opts.Link.Token)
	if err != nil {
		return ReceiveResult{}, err
	}
	result := ReceiveResult{State: owner.State, ExpiresAt: owner.ExpiresAt}

	if opts.Revoke {
		if err := client.RevokeRequest(ctx, opts.Link.ID, opts.Link.Token); err != nil {
			return result, err
		}
		result.State = "cancelled"
		return result, nil
	}
	if opts.StatusOnly || owner.State != "submitted" {
		return result, nil
	}

	identity := opts.Link.Identity
	if identity == nil {
		return result, errors.New("this retrieval link carries no key (#key=…); without it the reply cannot be decrypted")
	}
	if got := clientcrypto.RecipientFingerprint(clientcrypto.RecipientStringOf(identity)); got != owner.Fingerprint {
		return result, errors.New("the reply was encrypted to a different key than the one in this link; refusing to open")
	}

	limits := client.Config(ctx)
	openOpts := OpenOptions{OutputPath: opts.OutputPath, OutputDir: opts.OutputDir, Reveal: opts.Reveal}
	reserved, err := reserveDestination(openOpts, "text", "flick-request-"+opts.Link.ID)
	if err != nil {
		return result, err
	}
	defer reserved.close()

	opened, err := client.OpenRequest(ctx, opts.Link.ID, opts.Link.Token)
	if err != nil {
		reserved.discard()
		return result, err
	}
	defer opened.Body.Close()

	if opened.Kind != "file" {
		capped := &countingReader{r: opened.Body, limit: ageFileBytes(limits.PayloadInlineMaxBytes)}
		openRes, err := openText(openOpts, reserved, identity, capped, OpenResult{Kind: opened.Kind})
		if err != nil {
			// The result carries the plaintext or rescued path for the caller to salvage.
			if errors.Is(err, ErrWriteAfterConsume) {
				result.Opened = &openRes
			}
			return result, err
		}
		if capped.over {
			// The reply was consumed and secured; surface it with the cap notice.
			result.Opened = &openRes
			return result, ErrOverInlineCap
		}
		result.Opened = &openRes
		return result, nil
	}

	// The auto-reserved text name is wrong for a file; the directory is already proven writable.
	if openOpts.OutputPath == "" {
		reserved.discard()
	}
	openRes, err := openFile(openOpts, reserved, identity, opened.EncryptedFilename, opened.Body, OpenResult{Kind: opened.Kind})
	if err != nil {
		// The result carries the rescued staging path for the caller to salvage.
		if errors.Is(err, ErrWriteAfterConsume) {
			result.Opened = &openRes
		}
		return result, err
	}
	result.Opened = &openRes
	return result, nil
}

func sealToRecipient(recipient string, plaintext []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := clientcrypto.EncryptToRecipient(&out, recipient)
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

// sealFileToRecipient streams the file into a ciphertext temp file, mirroring
// sealFile for secrets; neither direction holds a whole file in memory.
func sealFileToRecipient(recipient string, src io.Reader, limit int64) (*sealedFile, error) {
	staged, err := os.CreateTemp("", "flick-submit-*.age")
	if err != nil {
		return nil, fmt.Errorf("stage encrypted file: %w", err)
	}
	sealed := &sealedFile{file: staged}
	w, err := clientcrypto.EncryptToRecipient(staged, recipient)
	if err != nil {
		sealed.remove()
		return nil, err
	}
	if sealed.plaintextBytes, err = io.Copy(w, io.LimitReader(src, limit+1)); err != nil {
		sealed.remove()
		return nil, fmt.Errorf("encrypt file: %w", err)
	}
	if sealed.plaintextBytes > limit {
		sealed.remove()
		return nil, fmt.Errorf("file is larger than %d bytes, the most this deployment accepts", limit)
	}
	if err := w.Close(); err != nil {
		sealed.remove()
		return nil, fmt.Errorf("encrypt file: %w", err)
	}
	if sealed.size, err = staged.Seek(0, io.SeekCurrent); err != nil {
		sealed.remove()
		return nil, err
	}
	_, err = staged.Seek(0, io.SeekStart)
	return sealed, err
}

func openNote(memoIdentity *age.HybridIdentity, encoded string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("the requester's note is not valid base64: %w", err)
	}
	reader, err := clientcrypto.DecryptWith(bytes.NewReader(sealed), memoIdentity)
	if err != nil {
		return "", fmt.Errorf("the requester's note could not be decrypted: %w", err)
	}
	note, err := io.ReadAll(io.LimitReader(reader, RequestNoteMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("the requester's note could not be read: %w", err)
	}
	if len(note) > RequestNoteMaxBytes {
		return "", errors.New("the requester's note is larger than the contract allows")
	}
	return string(note), nil
}

type countingReader struct {
	r     io.Reader
	n     int64
	limit int64
	over  bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		c.over = true
	}
	return n, err
}
