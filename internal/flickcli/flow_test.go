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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// fakeFlick is a stand-in for the API and the object store: enough of
// contracts/openapi.yaml to drive the client end to end, plus a record of every
// request body so a test can assert what the server was told.
type fakeFlick struct {
	mu sync.Mutex

	server *httptest.Server

	inlineMax int64
	maxFile   int64
	// s3Enabled controls whether a ciphertext-less create gets a presigned
	// upload back, mirroring a deployment without object storage.
	s3Enabled bool

	secrets map[string]*storedSecret
	objects map[string][]byte

	createBodies []string
	openCalls    int
	nextID       int
}

type storedSecret struct {
	kind              string
	ciphertext        string
	wrappedIdentity   string
	accessProofHash   string
	accessKDF         *clientcrypto.KDFParams
	encryptedFilename string
	contentType       string
	sizeBytes         int
	pendingUpload     bool
	consumed          bool
}

func newFakeFlick(t *testing.T) *fakeFlick {
	t.Helper()
	fake := &fakeFlick{
		inlineMax: DefaultPayloadInlineMaxBytes,
		maxFile:   DefaultMaxFileBytes,
		s3Enabled: true,
		secrets:   map[string]*storedSecret{},
		objects:   map[string][]byte{},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeFlick) client() *Client { return NewClient(f.server.URL, f.server.Client()) }

func (f *fakeFlick) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/config":
		writeJSON(w, http.StatusOK, Limits{PayloadInlineMaxBytes: f.inlineMax, MaxFileBytes: f.maxFile})
	case path == "/api/secrets" && r.Method == http.MethodPost:
		f.createSecret(w, r)
	case strings.HasSuffix(path, "/finalize"):
		f.finalize(w, strings.TrimSuffix(strings.TrimPrefix(path, "/api/secrets/"), "/finalize"))
	case strings.HasSuffix(path, "/open"):
		f.open(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/api/secrets/"), "/open"))
	case strings.HasPrefix(path, "/api/secrets/"):
		f.metadata(w, strings.TrimPrefix(path, "/api/secrets/"))
	case strings.HasPrefix(path, "/bucket/"):
		f.putObject(w, r, strings.TrimPrefix(path, "/bucket/"))
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (f *fakeFlick) createSecret(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	var req CreateSecretRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.createBodies = append(f.createBodies, string(raw))
	f.nextID++
	id := fmt.Sprintf("secret%d", f.nextID)

	// The real API checks an inline body's exact length and age header the
	// same way (internal/secrets/store.go validateCreate).
	if req.Ciphertext != "" {
		body, err := base64.StdEncoding.DecodeString(req.Ciphertext)
		if err != nil || len(body) != req.SizeBytes || !strings.HasPrefix(string(body), "age-encryption.org/v1\n") {
			writeError(w, http.StatusBadRequest, "invalid_ciphertext")
			return
		}
		// The server's default inline cap on the age file, which the
		// client's plaintext fallback limit has to respect.
		if len(body) > 1_048_576 {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
	}
	stored := &storedSecret{
		kind:              req.Kind,
		ciphertext:        req.Ciphertext,
		wrappedIdentity:   req.WrappedIdentity,
		encryptedFilename: req.EncryptedFilename,
		contentType:       req.ContentType,
		sizeBytes:         req.SizeBytes,
	}
	if req.Access != nil {
		stored.accessProofHash = req.Access.Proof
		stored.accessKDF = &req.Access.KDF
	}

	// New servers add management fields; the existing CLI must ignore them.
	resp := struct {
		CreateSecretResponse
		ManagementToken     string `json:"management_token"`
		ManagementExpiresAt string `json:"management_expires_at"`
	}{CreateSecretResponse{ID: id, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}, "opaque-management-token", time.Now().Add(time.Hour).Format(time.RFC3339)}
	if req.Ciphertext == "" {
		if !f.s3Enabled {
			// A deployment without object storage simply returns no upload,
			// which is exactly the case the client has to detect.
			f.secrets[id] = stored
			writeJSON(w, http.StatusCreated, resp)
			return
		}
		stored.pendingUpload = true
		// Content-Length is signed and equals size_bytes, the exact age file
		// length; the fake bucket enforces it the way a real one would.
		resp.Upload = &PresignedUpload{
			URL:       f.server.URL + "/bucket/" + id,
			Method:    http.MethodPut,
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			Headers: map[string]string{
				"Content-Length": fmt.Sprint(req.SizeBytes),
				"X-Flick-Signed": "yes",
			},
		}
	}
	f.secrets[id] = stored
	writeJSON(w, http.StatusCreated, resp)
}

func (f *fakeFlick) putObject(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.secrets[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Header.Get("X-Flick-Signed") != "yes" {
		// Dropping a signed header would break the signature at a real bucket.
		writeError(w, http.StatusForbidden, "signature_mismatch")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed")
		return
	}
	if int(r.ContentLength) != stored.sizeBytes {
		writeError(w, http.StatusForbidden, "signature_mismatch")
		return
	}
	f.objects[id] = body
	w.WriteHeader(http.StatusOK)
}

func (f *fakeFlick) finalize(w http.ResponseWriter, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.secrets[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	object, uploaded := f.objects[id]
	if !uploaded {
		writeError(w, http.StatusUnprocessableEntity, "upload_failed")
		return
	}
	// The server reads the object back on open, so the fake stores it the same
	// way the API would return it.
	stored.ciphertext = base64.StdEncoding.EncodeToString(object)
	stored.pendingUpload = false
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "finalized": true})
}

func (f *fakeFlick) metadata(w http.ResponseWriter, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.secrets[id]
	if !ok || stored.consumed {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	resp := SecretMetadata{
		ID:        id,
		Kind:      stored.kind,
		SizeBytes: stored.sizeBytes,
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	if stored.accessKDF != nil {
		resp.Access = &accessMetadata{KDF: *stored.accessKDF}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fakeFlick) open(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		AccessProof string `json:"access_proof"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++

	stored, ok := f.secrets[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if stored.consumed {
		writeError(w, http.StatusGone, "consumed")
		return
	}
	// Model A verifies before consuming, so a wrong passphrase does not burn
	// the secret. Model B has nothing to verify.
	if stored.accessProofHash != "" && req.AccessProof != stored.accessProofHash {
		writeError(w, http.StatusForbidden, "invalid_access")
		return
	}
	stored.consumed = true

	// The open answers with the raw age body and Flick-* headers
	// (contracts/openapi.yaml openSecret).
	body, _ := base64.StdEncoding.DecodeString(stored.ciphertext)
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Flick-Kind", stored.kind)
	header.Set("Flick-Expires-At", time.Now().Add(time.Hour).Format(time.RFC3339))
	if stored.contentType != "" {
		header.Set("Flick-Content-Type", stored.contentType)
	}
	if stored.encryptedFilename != "" {
		header.Set("Flick-Encrypted-Filename", stored.encryptedFilename)
	}
	if stored.wrappedIdentity != "" {
		header.Set("Flick-Wrapped-Identity", stored.wrappedIdentity)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func TestSendAndOpenModelBText(t *testing.T) {
	fake := newFakeFlick(t)

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		Text: "sk-live-not-a-real-key",
		TTL:  DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Model != "link-key" {
		t.Errorf("model = %q, want link-key", sent.Model)
	}
	if !strings.Contains(sent.Link, "#"+clientcrypto.FragmentKeyPrefix) {
		t.Errorf("link %q carries no fragment key", sent.Link)
	}

	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened.Plaintext) != "sk-live-not-a-real-key" {
		t.Errorf("plaintext = %q", opened.Plaintext)
	}

	// One view only: the second open must be refused.
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link}); err == nil {
		t.Error("a consumed secret opened a second time")
	}
}

func TestSendAndOpenModelAText(t *testing.T) {
	fake := newFakeFlick(t)

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		Text:       "the database password",
		Passphrase: "correct horse battery staple",
		TTL:        DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Model != "passphrase" {
		t.Errorf("model = %q, want passphrase", sent.Model)
	}
	// The key is derived from the passphrase, so putting it in the link would
	// make the passphrase pointless.
	if strings.Contains(sent.Link, "#") {
		t.Errorf("model A link %q carries a fragment", sent.Link)
	}

	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	opened, err := Open(context.Background(), fake.client(), OpenOptions{
		Link:       link,
		Passphrase: func() (string, error) { return "correct horse battery staple", nil },
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened.Plaintext) != "the database password" {
		t.Errorf("plaintext = %q", opened.Plaintext)
	}
}

// The security model's one rule: the server never learns the plaintext, the
// passphrase, or the derived key.
func TestServerNeverReceivesPlaintextOrPassphrase(t *testing.T) {
	fake := newFakeFlick(t)

	const plaintext = "correct-horse-plaintext-marker"
	const passphrase = "passphrase-marker-value"
	for _, opts := range []SendOptions{
		{Text: plaintext, Passphrase: passphrase, TTL: DefaultTTL},
		{Text: plaintext, TTL: DefaultTTL},
	} {
		if _, err := Send(context.Background(), fake.client(), opts); err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.createBodies) != 2 {
		t.Fatalf("create calls = %d, want 2", len(fake.createBodies))
	}
	for i, body := range fake.createBodies {
		// The identity, in the clear, would let the server decrypt the body.
		for _, forbidden := range []string{plaintext, passphrase, clientcrypto.IdentityPrefix} {
			if strings.Contains(body, forbidden) {
				t.Errorf("create request %d leaked %q", i, forbidden)
			}
		}
		// The server-side guard also rejects these field names outright
		// (hasSensitiveField in internal/httpapi/secrets.go); nonce and kdf
		// belonged to the removed AES-GCM format.
		for _, field := range []string{`"passphrase"`, `"plaintext"`, `"key"`, `"nonce"`, `"kdf":`} {
			if strings.Contains(strings.Replace(body, `"access":{"kdf":`, "", 1), field) {
				t.Errorf("create request %d carries a %s field", i, field)
			}
		}
	}
	// Model A sends the wrapped identity; Model B must not.
	if !strings.Contains(fake.createBodies[0], `"wrapped_identity":`) {
		t.Error("model A create request carries no wrapped identity")
	}
	if strings.Contains(fake.createBodies[1], `"wrapped_identity":`) || strings.Contains(fake.createBodies[1], `"access":`) {
		t.Error("model B create request carries Model A fields")
	}
}

func TestSendLargeFileUsesPresignedUploadAndFinalizes(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64 // force the large path without building a 1 MiB fixture

	// Several 64 KiB age chunks, so the streamed encrypt and decrypt cross
	// chunk boundaries.
	payload := []byte(strings.Repeat("large file body. ", 12_000))
	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader(payload),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !sent.Uploaded {
		t.Error("large file did not take the presigned upload path")
	}

	fake.mu.Lock()
	body := fake.createBodies[0]
	_, uploaded := fake.objects[sent.ID]
	pending := fake.secrets[sent.ID].pendingUpload
	fake.mu.Unlock()

	if strings.Contains(body, `"ciphertext"`) {
		t.Error("large create request still carried the ciphertext inline")
	}
	if !uploaded {
		t.Error("ciphertext never reached the object store")
	}
	if pending {
		t.Error("secret was left pending after finalize")
	}

	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	dir := t.TempDir()
	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.Filename != "report.pdf" {
		t.Errorf("filename = %q", opened.Filename)
	}
	written, err := os.ReadFile(opened.WrittenPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(written) != string(payload) {
		t.Error("file contents did not survive the round trip")
	}
}

func TestSendFileUsesPlaintextInlineBoundaryWhenConfigUnavailable(t *testing.T) {
	for _, size := range []int{1_044_208, 1_044_209} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			fake := newFakeFlick(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/config" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fake.route(w, r)
			}))
			t.Cleanup(server.Close)
			client := NewClient(server.URL, server.Client())
			limits := client.Config(context.Background())
			if limits.PayloadInlineMaxBytes != 1_044_208 || limits.MaxFileBytes != 268_435_456 {
				t.Fatalf("fallback limits = %+v, want plaintext inline/file limits", limits)
			}
			sent, err := Send(context.Background(), client, SendOptions{
				FileName: "boundary.bin",
				File:     bytes.NewReader(make([]byte, size)),
				TTL:      DefaultTTL,
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if want := size > 1_044_208; sent.Uploaded != want {
				t.Errorf("uploaded = %v, want %v for %d plaintext bytes", sent.Uploaded, want, size)
			}
		})
	}
}

func TestSendLargeFileFailsClearlyWithoutObjectStorage(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64
	fake.s3Enabled = false

	_, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader([]byte(strings.Repeat("x", 256))),
		TTL:      DefaultTTL,
	})
	if err == nil {
		t.Fatal("expected an error when the server returns no presigned upload")
	}
	if !strings.Contains(err.Error(), "object storage") {
		t.Errorf("error %q does not name the missing object storage", err)
	}
}

func TestSendRejectsFilesOverTheDeploymentLimit(t *testing.T) {
	fake := newFakeFlick(t)
	fake.maxFile = 100

	_, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "big.bin",
		File:     bytes.NewReader(make([]byte, 200)),
		TTL:      DefaultTTL,
	})
	if err == nil {
		t.Fatal("expected an oversized file to be rejected")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// Rejected before any network call, exactly as the browser client does.
	if len(fake.createBodies) != 0 {
		t.Error("an oversized file was still sent to the server")
	}
}

func TestSendRejectsTextOverTheInlineLimit(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 100

	ctx := context.Background()

	// Text at exactly the limit succeeds.
	_, err := Send(ctx, fake.client(), SendOptions{
		Text: strings.Repeat("a", 100),
		TTL:  DefaultTTL,
	})
	if err != nil {
		t.Fatalf("text at the inline limit failed: %v", err)
	}

	fake.mu.Lock()
	if len(fake.createBodies) != 1 {
		t.Fatalf("expected 1 create call for text at limit, got %d", len(fake.createBodies))
	}
	fake.mu.Unlock()

	// Text 1 byte over the limit fails before any create request is made.
	_, err = Send(ctx, fake.client(), SendOptions{
		Text: strings.Repeat("a", 101),
		TTL:  DefaultTTL,
	})
	if err == nil {
		t.Fatal("expected text 1 byte over limit to be rejected")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.createBodies) != 1 {
		t.Errorf("expected still 1 create call, got %d (oversized text made a request)", len(fake.createBodies))
	}
}

// A sender-supplied filename must not be able to write outside the output
// directory. The name is decrypted from the payload, so it is attacker input.
func TestOpenConfinesSenderSuppliedFilename(t *testing.T) {
	fake := newFakeFlick(t)
	dir := t.TempDir()

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "../../escaped.txt",
		File:     bytes.NewReader([]byte("payload")),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if filepath.Dir(opened.WrittenPath) != dir {
		t.Errorf("wrote outside the output directory: %s", opened.WrittenPath)
	}
	if filepath.Base(opened.WrittenPath) != "escaped.txt" {
		t.Errorf("written file = %q, want escaped.txt", filepath.Base(opened.WrittenPath))
	}
}

// Overwriting silently would destroy a local file; failing would destroy the
// secret, which is already consumed by then. Neither is acceptable, so the
// write picks a free name and reports it.
func TestOpenNeverOverwritesAnExistingFile(t *testing.T) {
	fake := newFakeFlick(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(existing, []byte("do not clobber"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "notes.txt",
		File:     bytes.NewReader([]byte("the secret payload")),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.WrittenPath == existing {
		t.Fatal("the secret overwrote an existing file")
	}
	untouched, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if string(untouched) != "do not clobber" {
		t.Error("the existing file was modified")
	}
	recovered, err := os.ReadFile(opened.WrittenPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(recovered) != "the secret payload" {
		t.Errorf("written payload = %q", recovered)
	}
}

// When the write really cannot happen the secret is already consumed, so the
// error must arrive with the payload attached — cmd/flick keys off
// ErrWriteAfterConsume to salvage it instead of discarding it.
func TestOpenReportsTheUnwritablePayloadInsteadOfLosingIt(t *testing.T) {
	fake := newFakeFlick(t)
	dir := t.TempDir()
	// Every name writePayload would try, taken: "notes.txt" and its 99
	// numbered variants.
	for attempt := range 100 {
		taken := filepath.Join(dir, "notes.txt")
		if attempt > 0 {
			taken = numberedPath(taken, attempt)
		}
		if err := os.WriteFile(taken, []byte("taken"), 0o600); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "notes.txt",
		File:     bytes.NewReader([]byte("the secret payload")),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if !errors.Is(err, ErrWriteAfterConsume) {
		t.Fatalf("err = %v, want it to wrap ErrWriteAfterConsume", err)
	}
	// The file was streamed to disk, so the rescue is the fully decrypted
	// staging file, named in the result.
	rescued, err := os.ReadFile(result.RescuedPath)
	if err != nil {
		t.Fatalf("payload was lost with the error: read rescued path %q: %v", result.RescuedPath, err)
	}
	if string(rescued) != "the secret payload" {
		t.Errorf("rescued payload = %q", rescued)
	}
	if filepath.Dir(result.RescuedPath) != dir {
		t.Errorf("rescued %s outside the output directory %s", result.RescuedPath, dir)
	}
}

// A Model B link without its fragment cannot be decrypted, so the client must
// notice before the open consumes the secret.
func TestOpenWithoutFragmentKeyDoesNotConsumeTheSecret(t *testing.T) {
	fake := newFakeFlick(t)

	sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	stripped, _, _ := strings.Cut(sent.Link, "#")
	link, err := ParseShareLink(stripped)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link}); err == nil {
		t.Fatal("expected an error for a link with no fragment key")
	}

	fake.mu.Lock()
	openCalls := fake.openCalls
	fake.mu.Unlock()
	if openCalls != 0 {
		t.Errorf("open endpoint was called %d times; the secret was burned for nothing", openCalls)
	}

	// The secret is still there for a correctly formed link.
	full, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: full}); err != nil {
		t.Errorf("secret was not recoverable afterwards: %v", err)
	}
}

func TestOpenRejectsAWrongPassphraseWithoutConsuming(t *testing.T) {
	fake := newFakeFlick(t)

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		Text:       "payload",
		Passphrase: "right",
		TTL:        DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	_, err = Open(context.Background(), fake.client(), OpenOptions{
		Link:       link,
		Passphrase: func() (string, error) { return "wrong", nil },
	})
	if err == nil {
		t.Fatal("a wrong passphrase opened the secret")
	}

	// The server verifies the proof before consuming, so the right passphrase
	// still works afterwards.
	opened, err := Open(context.Background(), fake.client(), OpenOptions{
		Link:       link,
		Passphrase: func() (string, error) { return "right", nil },
	})
	if err != nil {
		t.Fatalf("open with the right passphrase: %v", err)
	}
	if string(opened.Plaintext) != "payload" {
		t.Errorf("plaintext = %q", opened.Plaintext)
	}
}

func TestSendRejectsTTLOutsideTheContractBounds(t *testing.T) {
	fake := newFakeFlick(t)

	for name, ttl := range map[string]time.Duration{
		"below minimum": MinTTL - time.Second,
		"above maximum": MaxTTL + time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Send(context.Background(), fake.client(), SendOptions{Text: "x", TTL: ttl}); err == nil {
				t.Error("accepted a TTL outside the contract bounds")
			}
		})
	}
}

// -output must not truncate an existing file. The old path used os.WriteFile,
// so opening a text secret into an existing name destroyed it — and if that
// write then failed, the secret was gone from the server too.
func TestOpenWithOutputPathNeverTruncatesAnExistingFile(t *testing.T) {
	fake := newFakeFlick(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(existing, []byte("do not clobber"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: existing}); err == nil {
		t.Fatal("expected an error rather than an overwrite")
	}

	untouched, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if string(untouched) != "do not clobber" {
		t.Error("the existing file was overwritten")
	}

	// Refused before the consuming call, so the secret still exists.
	fake.mu.Lock()
	openCalls := fake.openCalls
	fake.mu.Unlock()
	if openCalls != 0 {
		t.Errorf("open endpoint was called %d times; the secret was burned for nothing", openCalls)
	}
}

// An unusable output directory must be caught before the open, not after: the
// secret is destroyed by the open, and a typo in -output-dir would otherwise
// take the payload with it.
func TestOpenChecksTheOutputDirectoryBeforeConsuming(t *testing.T) {
	fake := newFakeFlick(t)

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader([]byte("payload")),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "no-such-directory")
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: missing}); err == nil {
		t.Fatal("expected an error for a missing output directory")
	}

	fake.mu.Lock()
	openCalls := fake.openCalls
	fake.mu.Unlock()
	if openCalls != 0 {
		t.Errorf("open endpoint was called %d times before the directory check", openCalls)
	}

	// The secret survived, so a corrected directory still works.
	dir := t.TempDir()
	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err != nil {
		t.Fatalf("open after fixing the directory: %v", err)
	}
	if filepath.Dir(opened.WrittenPath) != dir {
		t.Errorf("wrote to %s, want a file under %s", opened.WrittenPath, dir)
	}
}

// A refused open must not leave the empty file that reserving the path created.
func TestOpenRemovesTheReservedFileWhenTheOpenFails(t *testing.T) {
	fake := newFakeFlick(t)
	target := filepath.Join(t.TempDir(), "secret.txt")

	sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	// Consume it once so the second open is refused after the path is reserved.
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link}); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: target}); err == nil {
		t.Fatal("a consumed secret opened a second time")
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a failed open left %s behind", target)
	}
}

func TestOpenWritesATextSecretToTheRequestedPath(t *testing.T) {
	fake := newFakeFlick(t)
	target := filepath.Join(t.TempDir(), "secret.txt")

	sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "written payload", TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}

	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: target})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.WrittenPath != target {
		t.Errorf("WrittenPath = %q, want %q", opened.WrittenPath, target)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(written) != "written payload" {
		t.Errorf("written payload = %q", written)
	}
	// A secret on disk must not be world-readable.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// A malformed or absent encrypted_filename arrives after the open has already
// consumed the secret. The contract allows the field to be absent, and the body
// decrypted fine, so the payload is written under a generic name rather than
// handed back as an error over a secret that no longer exists anywhere else.
func TestOpenWritesAFileWhoseNameCannotBeDecrypted(t *testing.T) {
	for name, envelope := range map[string]string{
		"malformed":  base64.StdEncoding.EncodeToString([]byte("not an age file")),
		"not base64": "%%%",
		"absent":     "",
	} {
		t.Run(name, func(t *testing.T) {
			// A name that was sent and failed is a tampering or corruption
			// signal; a name that was never sent is not. The two must not look
			// alike to the caller, which reports one and stays quiet about the
			// other.
			wantUnreadable := envelope != ""
			fake := newFakeFlick(t)
			dir := t.TempDir()

			sent, err := Send(context.Background(), fake.client(), SendOptions{
				FileName: "report.pdf",
				File:     bytes.NewReader([]byte("body survives a broken name")),
				TTL:      DefaultTTL,
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			fake.mu.Lock()
			fake.secrets[sent.ID].encryptedFilename = envelope
			fake.mu.Unlock()

			link, err := ParseShareLink(sent.Link)
			if err != nil {
				t.Fatalf("parse link: %v", err)
			}
			result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if want := filepath.Join(dir, "flick-file"); result.WrittenPath != want {
				t.Errorf("WrittenPath = %q, want the generic %q", result.WrittenPath, want)
			}
			if result.FilenameUnreadable != wantUnreadable {
				t.Errorf("FilenameUnreadable = %v, want %v", result.FilenameUnreadable, wantUnreadable)
			}
			written, err := os.ReadFile(result.WrittenPath)
			if err != nil {
				t.Fatalf("read written file: %v", err)
			}
			if string(written) != "body survives a broken name" {
				t.Errorf("written payload = %q", written)
			}
		})
	}
}

// With -output the caller named the path, so the decrypted filename is never
// used. Reading it anyway made an optional field cost the user an error, an
// empty file at the path they asked for, and a hunt for the salvaged copy.
func TestOpenWithOutputPathIgnoresAnUndecryptableFilename(t *testing.T) {
	fake := newFakeFlick(t)
	target := filepath.Join(t.TempDir(), "report.pdf")

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader([]byte("payload")),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	fake.mu.Lock()
	fake.secrets[sent.ID].encryptedFilename = base64.StdEncoding.EncodeToString([]byte("not an age file"))
	fake.mu.Unlock()

	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: target})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if result.WrittenPath != target {
		t.Errorf("WrittenPath = %q, want %q", result.WrittenPath, target)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(written) != "payload" {
		t.Errorf("written payload = %q", written)
	}
}

// A decryption failure after the open must not leave the reserved -output path
// behind as an empty file where the user asked for a secret, nor write any part
// of a body that fails to authenticate.
func TestOpenRemovesTheReservedFileWhenDecryptionFails(t *testing.T) {
	for name, break_ := range map[string]func(t *testing.T, fake *fakeFlick, id string, link *ShareLink){
		"wrong identity": func(t *testing.T, _ *fakeFlick, _ string, link *ShareLink) {
			other, err := clientcrypto.NewIdentity()
			if err != nil {
				t.Fatalf("identity: %v", err)
			}
			link.Identity = other
		},
		"altered final chunk": func(_ *testing.T, fake *fakeFlick, id string, _ *ShareLink) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			body, _ := base64.StdEncoding.DecodeString(fake.secrets[id].ciphertext)
			body[len(body)-1] ^= 0x01
			fake.secrets[id].ciphertext = base64.StdEncoding.EncodeToString(body)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeFlick(t)
			target := filepath.Join(t.TempDir(), "secret.txt")

			sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			link, err := ParseShareLink(sent.Link)
			if err != nil {
				t.Fatalf("parse link: %v", err)
			}
			break_(t, fake, sent.ID, &link)

			result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: target})
			if err == nil {
				t.Fatal("expected decryption to fail")
			}
			if len(result.Plaintext) != 0 {
				t.Errorf("returned %d plaintext bytes from a body that failed to decrypt", len(result.Plaintext))
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("a failed decryption left %s behind", target)
			}
		})
	}
}

// A file secret is decrypted into a staging file and moved into place only
// after age authenticates the final chunk. A body that fails must leave
// nothing in the output directory: no partial file, no staging file.
func TestOpenFileLeavesNothingWhenTheBodyFailsToAuthenticate(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64
	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader(bytes.Repeat([]byte("chunked body "), 20_000)),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	fake.mu.Lock()
	body, _ := base64.StdEncoding.DecodeString(fake.secrets[sent.ID].ciphertext)
	body[len(body)-1] ^= 0x01
	fake.secrets[sent.ID].ciphertext = base64.StdEncoding.EncodeToString(body)
	fake.mu.Unlock()

	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	dir := t.TempDir()
	result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err == nil {
		t.Fatal("an altered body opened")
	}
	if errors.Is(err, ErrWriteAfterConsume) || result.RescuedPath != "" {
		t.Errorf("a decryption failure was reported as a rescuable write failure: %v", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed open left %v in the output directory", entries)
	}
}

// The open body must be exactly the size_bytes the metadata reported; a
// substituted body of another length is refused.
func TestOpenRefusesABodyOfTheWrongLength(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"longer":  func(b []byte) []byte { return append(b, 0) },
		"shorter": func(b []byte) []byte { return b[:len(b)-1] },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeFlick(t)
			sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			fake.mu.Lock()
			body, _ := base64.StdEncoding.DecodeString(fake.secrets[sent.ID].ciphertext)
			fake.secrets[sent.ID].ciphertext = base64.StdEncoding.EncodeToString(change(body))
			fake.mu.Unlock()

			link, err := ParseShareLink(sent.Link)
			if err != nil {
				t.Fatalf("parse link: %v", err)
			}
			result, err := Open(context.Background(), fake.client(), OpenOptions{Link: link})
			if err == nil {
				t.Fatal("a body of the wrong length opened")
			}
			if len(result.Plaintext) != 0 {
				t.Errorf("returned plaintext %q from a body of the wrong length", result.Plaintext)
			}
		})
	}
}

func TestSendAndOpenModelAFile(t *testing.T) {
	fake := newFakeFlick(t)
	payload := bytes.Repeat([]byte{0, 1, 2, 250}, 40_000)

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName:   "keys.bin",
		File:       bytes.NewReader(payload),
		Passphrase: "correct horse battery staple",
		TTL:        DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	dir := t.TempDir()
	opened, err := Open(context.Background(), fake.client(), OpenOptions{
		Link:       link,
		OutputDir:  dir,
		Passphrase: func() (string, error) { return "correct horse battery staple", nil },
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if want := filepath.Join(dir, "keys.bin"); opened.WrittenPath != want {
		t.Errorf("WrittenPath = %q, want %q", opened.WrittenPath, want)
	}
	written, err := os.ReadFile(opened.WrittenPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Error("file contents did not survive the round trip")
	}
}

// A server reporting an impossible size must be refused before the consuming
// open: a negative size used to reach a slice bound after consumption.
func TestOpenRefusesAnImpossibleReportedSize(t *testing.T) {
	for name, size := range map[string]int{"negative": -1, "zero": 0, "text over inline": 1 << 30} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeFlick(t)
			sent, err := Send(context.Background(), fake.client(), SendOptions{Text: "payload", TTL: DefaultTTL})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			fake.mu.Lock()
			fake.secrets[sent.ID].sizeBytes = size
			fake.mu.Unlock()
			link, err := ParseShareLink(sent.Link)
			if err != nil {
				t.Fatalf("parse link: %v", err)
			}
			if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link}); err == nil {
				t.Fatal("opened a secret with an impossible reported size")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.openCalls != 0 {
				t.Errorf("open calls = %d, want the secret left unconsumed", fake.openCalls)
			}
		})
	}
}

// A placed file secret is 0600 and leaves no staging file behind.
func TestOpenFilePlacesAPrivateFileAndCleansUp(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64
	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName: "report.pdf",
		File:     bytes.NewReader(bytes.Repeat([]byte("x"), 70_000)),
		TTL:      DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	link, err := ParseShareLink(sent.Link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	dir := t.TempDir()
	opened, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputDir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	info, err := os.Stat(opened.WrittenPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.pdf" {
		t.Errorf("output directory holds %v, want only report.pdf", entries)
	}
}

func TestCopyExclusiveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyExclusive(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "payload" {
		t.Errorf("copied %q", got)
	}
	if err := copyExclusive(src, dst); !errors.Is(err, os.ErrExist) {
		t.Errorf("second copy err = %v, want os.ErrExist", err)
	}
}
