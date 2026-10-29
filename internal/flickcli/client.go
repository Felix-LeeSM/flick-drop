// Package flickcli implements the `flick` command-line client: the API calls,
// share-link handling, and the send/open flows. cmd/flick only parses flags and
// dispatches into here.
//
// The client is a peer of the SvelteKit app in web/src/lib/api, not a wrapper
// around the server's internals: it speaks the same public HTTP contract in
// contracts/openapi.yaml and does all encryption locally through
// internal/clientcrypto. It must never import internal/secrets, internal/db, or
// internal/storage — the CLI is an outside caller and gains nothing the browser
// could not also do.
package flickcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Default limits, in bytes before encryption, mirror web/src/lib/api/config.ts.
// The inline ceiling is the largest input whose age file fits the server's
// 1 MiB default: secrets.MaxPlaintextBytes(1048576) in internal/secrets/age.go.
// They are the fallback when GET /api/config is unreachable; the server
// re-enforces both, so these are advisory.
const (
	DefaultPayloadInlineMaxBytes = 1_044_208
	DefaultMaxFileBytes          = 52_428_800 // 50 MiB
)

// Client talks to one Flick deployment.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client for the given API base URL. Trailing slashes are
// trimmed so path joining stays predictable.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		// Generous but bounded: a 50 MiB upload on a slow link needs room, and
		// no timeout at all turns a hung bucket into a hung terminal.
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// BaseURL is the deployment this client points at.
func (c *Client) BaseURL() string { return c.baseURL }

// APIError carries the server's error code so callers can react to `consumed`
// or `invalid_access` without matching on message text.
type APIError struct {
	Code    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Limits are the client-facing size limits from GET /api/config.
type Limits struct {
	PayloadInlineMaxBytes int64 `json:"payload_inline_max_bytes"`
	MaxFileBytes          int64 `json:"max_file_bytes"`
}

// DefaultLimits is what the client falls back to when /api/config cannot be
// read. Matching the browser's fallback keeps routing decisions identical.
func DefaultLimits() Limits {
	return Limits{
		PayloadInlineMaxBytes: DefaultPayloadInlineMaxBytes,
		MaxFileBytes:          DefaultMaxFileBytes,
	}
}

// Config fetches the deployment's size limits. Like the browser client it never
// fails the caller: an unreachable or malformed config yields the defaults, and
// the server re-enforces the real limits anyway.
func (c *Client) Config(ctx context.Context) Limits {
	limits := DefaultLimits()
	var fetched Limits
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &fetched); err != nil {
		return limits
	}
	if fetched.PayloadInlineMaxBytes > 0 {
		limits.PayloadInlineMaxBytes = fetched.PayloadInlineMaxBytes
	}
	if fetched.MaxFileBytes > 0 {
		limits.MaxFileBytes = fetched.MaxFileBytes
	}
	return limits
}

// CreateSecretRequest mirrors contracts/openapi.yaml CreateSecretRequest. The
// API rejects unknown fields, and omitempty on Ciphertext is what selects the
// large-upload path: omitting it asks the server for a presigned PUT.
//
// Ciphertext, EncryptedFilename, and WrappedIdentity are base64 age files.
// SizeBytes is the exact length of the body age file.
type CreateSecretRequest struct {
	Kind              string                       `json:"kind"`
	Ciphertext        string                       `json:"ciphertext,omitempty"`
	Access            *clientcrypto.AccessVerifier `json:"access,omitempty"`
	WrappedIdentity   string                       `json:"wrapped_identity,omitempty"`
	EncryptedFilename string                       `json:"encrypted_filename,omitempty"`
	ContentType       string                       `json:"content_type,omitempty"`
	SizeBytes         int                          `json:"size_bytes"`
	TTLSeconds        int                          `json:"ttl_seconds"`
	MaxViews          int                          `json:"max_views"`
}

// PresignedUpload is a signed request for sending ciphertext straight to the
// object store, so the server never handles the bytes.
type PresignedUpload struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	ExpiresAt string            `json:"expires_at"`
	Headers   map[string]string `json:"headers"`
}

type CreateSecretResponse struct {
	ID        string           `json:"id"`
	ExpiresAt string           `json:"expires_at"`
	Upload    *PresignedUpload `json:"upload,omitempty"`
}

type accessMetadata struct {
	KDF clientcrypto.KDFParams `json:"kdf"`
}

// SecretMetadata is the pre-open probe. Access is nil for Model B secrets,
// which is how the CLI knows whether to ask for a passphrase at all.
type SecretMetadata struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Access    *accessMetadata `json:"access,omitempty"`
	SizeBytes int             `json:"size_bytes"`
	ExpiresAt string          `json:"expires_at"`
}

// NeedsPassphrase reports whether this is a Model A secret, which cannot be
// opened without deriving an access proof from a passphrase.
func (m SecretMetadata) NeedsPassphrase() bool { return m.Access != nil }

// AccessKDF is the KDF block the access proof must be derived under.
func (m SecretMetadata) AccessKDF() clientcrypto.KDFParams {
	if m.Access == nil {
		return clientcrypto.KDFParams{}
	}
	return m.Access.KDF
}

// OpenedSecret is what a successful open returns: the raw age body and the
// metadata the API sends in Flick-* headers (contracts/openapi.yaml
// openSecret). The open is destructive: the server consumes the secret in the
// same operation, so a failure to write the result locally loses it for good.
type OpenedSecret struct {
	Kind        string
	ContentType string
	ExpiresAt   string
	// EncryptedFilename is the base64 age file for file secrets, empty
	// otherwise. It stays encoded so a damaged name costs only the name: by
	// the time it is read the secret is already consumed.
	EncryptedFilename string
	// WrappedIdentity is the Model A age scrypt file, nil for Model B.
	WrappedIdentity []byte
	// Body streams the age file. It yields exactly the size_bytes the
	// metadata probe reported, or an error. The caller must Close it.
	Body io.ReadCloser
}

func (c *Client) CreateSecret(ctx context.Context, req CreateSecretRequest) (CreateSecretResponse, error) {
	var resp CreateSecretResponse
	err := c.do(ctx, http.MethodPost, "/api/secrets", req, &resp)
	return resp, err
}

func (c *Client) SecretMetadata(ctx context.Context, id string) (SecretMetadata, error) {
	var resp SecretMetadata
	err := c.do(ctx, http.MethodGet, "/api/secrets/"+url.PathEscape(id), nil, &resp)
	return resp, err
}

// OpenSecret consumes the secret. accessProof is empty for Model B. sizeBytes
// is the exact body length from the metadata probe; a body of any other length
// fails while it is read, so a substituted body cannot run on without bound.
func (c *Client) OpenSecret(ctx context.Context, id, accessProof string, sizeBytes int) (OpenedSecret, error) {
	body := map[string]string{}
	if accessProof != "" {
		body["access_proof"] = accessProof
	}
	if sizeBytes <= 0 {
		return OpenedSecret{}, fmt.Errorf("secret metadata reports an impossible size %d", sizeBytes)
	}
	resp, err := c.send(ctx, http.MethodPost, "/api/secrets/"+url.PathEscape(id)+"/open", body)
	if err != nil {
		return OpenedSecret{}, err
	}
	opened, err := openedSecret(resp, sizeBytes)
	if err != nil {
		resp.Body.Close()
		return OpenedSecret{}, err
	}
	return opened, nil
}

func openedSecret(resp *http.Response, sizeBytes int) (OpenedSecret, error) {
	var err error

	opened := OpenedSecret{
		Kind:        resp.Header.Get("Flick-Kind"),
		ContentType: resp.Header.Get("Flick-Content-Type"),
		ExpiresAt:   resp.Header.Get("Flick-Expires-At"),
	}
	if opened.Kind != "text" && opened.Kind != "file" {
		return OpenedSecret{}, fmt.Errorf("open response has an unknown kind %q", opened.Kind)
	}
	opened.EncryptedFilename = resp.Header.Get("Flick-Encrypted-Filename")
	if opened.WrappedIdentity, err = headerBytes(resp.Header, "Flick-Wrapped-Identity"); err != nil {
		return OpenedSecret{}, err
	}
	opened.Body = &exactBody{body: resp.Body, remaining: int64(sizeBytes)}
	return opened, nil
}

// exactBody reads exactly remaining bytes from body and fails on a shorter or
// longer stream.
type exactBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *exactBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		if n, _ := b.body.Read(extra[:]); n > 0 {
			return 0, errors.New("opened secret is longer than its recorded size")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if errors.Is(err, io.EOF) {
		if b.remaining > 0 {
			return n, fmt.Errorf("opened secret ended %d bytes short: %w", b.remaining, io.ErrUnexpectedEOF)
		}
		err = nil
	}
	if err != nil {
		return n, fmt.Errorf("read opened secret: %w", err)
	}
	return n, nil
}

func (b *exactBody) Close() error { return b.body.Close() }

// headerBytes decodes an optional base64 Flick-* header.
func headerBytes(header http.Header, name string) ([]byte, error) {
	value := header.Get(name)
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("open response header %s is not valid base64: %w", name, err)
	}
	return decoded, nil
}

// FinalizeSecret activates a large secret after its ciphertext has landed in
// the bucket. Until this call succeeds the secret is not openable.
func (c *Client) FinalizeSecret(ctx context.Context, id string) error {
	var resp struct {
		ID        string `json:"id"`
		Finalized bool   `json:"finalized"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/secrets/"+url.PathEscape(id)+"/finalize", struct{}{}, &resp); err != nil {
		return err
	}
	if !resp.Finalized {
		return &APIError{Code: "upload_failed", Message: "server did not finalize the upload"}
	}
	return nil
}

// Upload sends the raw age file to the presigned URL. Unlike the browser, a Go
// client may set Content-Length, so every signed header is echoed verbatim —
// Content-Length is part of the signature and a body of any other length is
// rejected at the bucket. body is read once, as it is sent.
func (c *Client) Upload(ctx context.Context, upload PresignedUpload, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, upload.Method, upload.URL, body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	for name, value := range upload.Headers {
		if strings.EqualFold(name, "Content-Length") {
			// net/http derives Content-Length from ContentLength, not the
			// header map; setting it here would be ignored.
			continue
		}
		req.Header.Set(name, value)
	}
	req.ContentLength = size

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload ciphertext: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &APIError{
			Code:    "upload_failed",
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("object store rejected the upload: %s", strings.TrimSpace(string(detail))),
		}
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

// send makes a JSON request and returns a 2xx response for the caller to read
// and close. Any other status becomes an *APIError.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", c.baseURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func readAPIError(resp *http.Response) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	// A non-JSON body (a proxy's HTML error page, say) still has to produce a
	// usable error, so decoding failures fall through to the status code.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&envelope)
	code := envelope.Error.Code
	if code == "" {
		code = "request_failed"
	}
	return &APIError{Code: code, Status: resp.StatusCode, Message: envelope.Error.Message}
}
