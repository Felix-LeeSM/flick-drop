package flickcli

import (
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
	nonce             string
	kdf               *clientcrypto.KDFParams
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

	stored := &storedSecret{
		kind:              req.Kind,
		ciphertext:        req.Ciphertext,
		nonce:             req.Nonce,
		kdf:               req.KDF,
		encryptedFilename: req.EncryptedFilename,
		contentType:       req.ContentType,
		sizeBytes:         req.SizeBytes,
	}
	if req.Access != nil {
		stored.accessProofHash = req.Access.Proof
		stored.accessKDF = &req.Access.KDF
	}

	resp := CreateSecretResponse{ID: id, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if req.Ciphertext == "" {
		if !f.s3Enabled {
			// A deployment without object storage simply returns no upload,
			// which is exactly the case the client has to detect.
			f.secrets[id] = stored
			writeJSON(w, http.StatusCreated, resp)
			return
		}
		stored.pendingUpload = true
		// Content-Length is signed and equals plaintext size plus the 16-byte
		// AEAD tag; the fake bucket enforces it the way a real one would.
		resp.Upload = &PresignedUpload{
			URL:       f.server.URL + "/bucket/" + id,
			Method:    http.MethodPut,
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			Headers: map[string]string{
				"Content-Length": fmt.Sprint(req.SizeBytes + 16),
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
	if int(r.ContentLength) != stored.sizeBytes+16 {
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

	resp := OpenSecretResponse{
		ID:                id,
		Kind:              stored.kind,
		Ciphertext:        stored.ciphertext,
		Nonce:             stored.nonce,
		KDF:               stored.kdf,
		EncryptedFilename: stored.encryptedFilename,
		ContentType:       stored.contentType,
		SizeBytes:         stored.sizeBytes,
		ExpiresAt:         time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
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
	if _, err := Send(context.Background(), fake.client(), SendOptions{
		Text:       plaintext,
		Passphrase: passphrase,
		TTL:        DefaultTTL,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.createBodies) != 1 {
		t.Fatalf("create calls = %d, want 1", len(fake.createBodies))
	}
	body := fake.createBodies[0]
	for _, forbidden := range []string{plaintext, passphrase} {
		if strings.Contains(body, forbidden) {
			t.Errorf("create request leaked %q", forbidden)
		}
	}
	// The server-side guard also rejects these field names outright
	// (hasSensitiveField in internal/httpapi/secrets.go).
	for _, field := range []string{`"passphrase"`, `"plaintext"`, `"key"`} {
		if strings.Contains(body, field) {
			t.Errorf("create request carries a %s field", field)
		}
	}
}

func TestSendLargeFileUsesPresignedUploadAndFinalizes(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64 // force the large path without building a 1 MiB fixture

	payload := []byte(strings.Repeat("large file body. ", 64))
	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName:  "report.pdf",
		FileBytes: payload,
		TTL:       DefaultTTL,
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

func TestSendLargeFileFailsClearlyWithoutObjectStorage(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 64
	fake.s3Enabled = false

	_, err := Send(context.Background(), fake.client(), SendOptions{
		FileName:  "report.pdf",
		FileBytes: []byte(strings.Repeat("x", 256)),
		TTL:       DefaultTTL,
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
		FileName:  "big.bin",
		FileBytes: make([]byte, 200),
		TTL:       DefaultTTL,
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

// A sender-supplied filename must not be able to write outside the output
// directory. The name is decrypted from the payload, so it is attacker input.
func TestOpenConfinesSenderSuppliedFilename(t *testing.T) {
	fake := newFakeFlick(t)
	dir := t.TempDir()

	sent, err := Send(context.Background(), fake.client(), SendOptions{
		FileName:  "../../escaped.txt",
		FileBytes: []byte("payload"),
		TTL:       DefaultTTL,
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
		FileName:  "notes.txt",
		FileBytes: []byte("the secret payload"),
		TTL:       DefaultTTL,
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
		FileName:  "notes.txt",
		FileBytes: []byte("the secret payload"),
		TTL:       DefaultTTL,
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
	if string(result.Plaintext) != "the secret payload" {
		t.Errorf("payload was lost with the error: %q", result.Plaintext)
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
		FileName:  "report.pdf",
		FileBytes: []byte("payload"),
		TTL:       DefaultTTL,
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
		"malformed": `{"nonce":"","ciphertext":""}`,
		"absent":    "",
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
				FileName:  "report.pdf",
				FileBytes: []byte("body survives a broken name"),
				TTL:       DefaultTTL,
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
		FileName:  "report.pdf",
		FileBytes: []byte("payload"),
		TTL:       DefaultTTL,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	fake.mu.Lock()
	fake.secrets[sent.ID].encryptedFilename = `{"nonce":"","ciphertext":""}`
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
// behind as an empty file where the user asked for a secret.
func TestOpenRemovesTheReservedFileWhenDecryptionFails(t *testing.T) {
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
	// A link whose fragment key is not the key the payload was sealed with.
	link.Key = make([]byte, clientcrypto.RawKeyBytes)

	if _, err := Open(context.Background(), fake.client(), OpenOptions{Link: link, OutputPath: target}); err == nil {
		t.Fatal("expected decryption to fail with the wrong key")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a failed decryption left %s behind", target)
	}
}
