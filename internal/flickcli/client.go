// Package flickcli is an outside client of contracts/openapi.yaml, a peer of web/src/lib/api.
// It must never import internal/secrets, internal/db, or internal/storage.
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

// Mirror web/src/lib/api/config.ts; fallback when GET /api/config is unreachable. The server re-enforces both.
const (
	DefaultPayloadInlineMaxBytes = 1_044_208
	DefaultMaxFileBytes          = 268_435_456 // 256 MiB
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		// Matches the API's 30-minute open stream limit.
		httpClient = &http.Client{Timeout: 30 * time.Minute}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

func (c *Client) BaseURL() string { return c.baseURL }

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

type Limits struct {
	PayloadInlineMaxBytes int64 `json:"payload_inline_max_bytes"`
	MaxFileBytes          int64 `json:"max_file_bytes"`
}

// Matching the browser's fallback keeps routing decisions identical.
func DefaultLimits() Limits {
	return Limits{
		PayloadInlineMaxBytes: DefaultPayloadInlineMaxBytes,
		MaxFileBytes:          DefaultMaxFileBytes,
	}
}

// Never fails, like the browser client: the server re-enforces the real limits.
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

// The API rejects unknown fields; omitting Ciphertext asks for a presigned PUT.
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

// Access is nil for Model B secrets.
type SecretMetadata struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Access    *accessMetadata `json:"access,omitempty"`
	SizeBytes int             `json:"size_bytes"`
	ExpiresAt string          `json:"expires_at"`
}

func (m SecretMetadata) NeedsPassphrase() bool { return m.Access != nil }

func (m SecretMetadata) AccessKDF() clientcrypto.KDFParams {
	if m.Access == nil {
		return clientcrypto.KDFParams{}
	}
	return m.Access.KDF
}

type OpenedSecret struct {
	Kind        string
	ContentType string
	ExpiresAt   string
	// Kept encoded so a damaged name costs only the name; the secret is already consumed when it is read.
	EncryptedFilename string
	WrappedIdentity   []byte
	// Yields exactly the probed size_bytes or an error. The caller must Close it.
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

// A body of any other length than sizeBytes fails while read, so a substituted body cannot run unbounded.
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

// Content-Length is part of the signature, so every signed header is echoed verbatim.
func (c *Client) Upload(ctx context.Context, upload PresignedUpload, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, upload.Method, upload.URL, body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	for name, value := range upload.Headers {
		if strings.EqualFold(name, "Content-Length") {
			// net/http takes Content-Length from ContentLength; the header map entry is ignored.
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
	// A non-JSON body (a proxy's HTML error page) falls through to the status code.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&envelope)
	code := envelope.Error.Code
	if code == "" {
		code = "request_failed"
	}
	return &APIError{Code: code, Status: resp.StatusCode, Message: envelope.Error.Message}
}
