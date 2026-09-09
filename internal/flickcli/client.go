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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Default limits mirror internal/config/defaults.go and web/src/lib/api/config.ts.
// They are the fallback when GET /api/config is unreachable; the server
// re-enforces both, so these are advisory.
const (
	DefaultPayloadInlineMaxBytes = 1_048_576  // 1 MiB
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
type CreateSecretRequest struct {
	Kind              string                       `json:"kind"`
	Ciphertext        string                       `json:"ciphertext,omitempty"`
	Nonce             string                       `json:"nonce"`
	KDF               *clientcrypto.KDFParams      `json:"kdf,omitempty"`
	Access            *clientcrypto.AccessVerifier `json:"access,omitempty"`
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

// OpenSecretResponse is the payload returned by a successful open. The open is
// destructive: the server consumes the secret in the same operation, so a
// failure to write the result locally loses it for good.
type OpenSecretResponse struct {
	ID                string                  `json:"id"`
	Kind              string                  `json:"kind"`
	Ciphertext        string                  `json:"ciphertext"`
	Nonce             string                  `json:"nonce"`
	KDF               *clientcrypto.KDFParams `json:"kdf,omitempty"`
	EncryptedFilename string                  `json:"encrypted_filename,omitempty"`
	ContentType       string                  `json:"content_type,omitempty"`
	SizeBytes         int                     `json:"size_bytes"`
	ExpiresAt         string                  `json:"expires_at"`
}

// Payload rebuilds the clientcrypto payload for decryption. The KDF is absent
// for Model B, where the key comes from the link fragment instead.
func (r OpenSecretResponse) Payload() clientcrypto.Payload {
	payload := clientcrypto.Payload{
		Ciphertext: r.Ciphertext,
		Nonce:      r.Nonce,
		SizeBytes:  r.SizeBytes,
	}
	if r.KDF != nil {
		payload.KDF = *r.KDF
	}
	return payload
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

// OpenSecret consumes the secret. accessProof is empty for Model B.
func (c *Client) OpenSecret(ctx context.Context, id, accessProof string) (OpenSecretResponse, error) {
	body := map[string]string{}
	if accessProof != "" {
		body["access_proof"] = accessProof
	}
	var resp OpenSecretResponse
	err := c.do(ctx, http.MethodPost, "/api/secrets/"+url.PathEscape(id)+"/open", body, &resp)
	return resp, err
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

// Upload sends raw ciphertext to the presigned URL. Unlike the browser, a Go
// client may set Content-Length, so every signed header is echoed verbatim —
// Content-Length is part of the signature and a body of any other length is
// rejected at the bucket.
func (c *Client) Upload(ctx context.Context, upload PresignedUpload, ciphertext []byte) error {
	req, err := http.NewRequestWithContext(ctx, upload.Method, upload.URL, bytes.NewReader(ciphertext))
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
	req.ContentLength = int64(len(ciphertext))

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
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
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
