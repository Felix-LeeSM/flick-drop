package flickcli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// The request handlers mirror internal/requests closely enough to exercise
// the CLI flows: bearer roles, the generation/attempt idempotency, the inline
// versus presigned split, and the one-shot consuming open.

type storedRequest struct {
	publicKey       string
	submissionToken string
	retrievalToken  string
	encryptedNote   string
	expiresAt       string
	generation      int
	state           string // waiting | submitted | consumed | cancelled

	kind          string
	envelope      RequestEnvelope
	ciphertext    []byte
	sizeBytes     int64
	attemptToken  string
	attemptDigest string

	reservedKey  string
	reservedSize int64
	uploaded     bool
}

func randomToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func (f *fakeFlick) createRequest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16384))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	f.requestBodies = append(f.requestBodies, string(raw))
	var req CreateRequestInput
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if _, err := clientcrypto.ParseRecipient(req.PublicKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRequestID++
	id := fmt.Sprintf("req%d", f.nextRequestID)
	f.requests[id] = &storedRequest{
		publicKey:       req.PublicKey,
		submissionToken: randomToken(),
		retrievalToken:  randomToken(),
		encryptedNote:   req.EncryptedNote,
		expiresAt:       time.Now().Add(time.Hour).Format(time.RFC3339),
		generation:      1,
		state:           "waiting",
	}
	writeJSON(w, http.StatusCreated, CreatedRequest{
		ID:              id,
		ExpiresAt:       f.requests[id].expiresAt,
		SubmissionToken: f.requests[id].submissionToken,
		RetrievalToken:  f.requests[id].retrievalToken,
	})
}

func requestBearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return "", false
	}
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return "", false
	}
	return token, true
}

func (f *fakeFlick) routeRequest(w http.ResponseWriter, r *http.Request, rest string) {
	token, ok := requestBearer(w, r)
	if !ok {
		return
	}
	id, action, _ := strings.Cut(rest, "/")

	f.mu.Lock()
	defer f.mu.Unlock()
	stored, found := f.requests[id]
	if !found {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}

	switch action {
	case "":
		if r.Method != http.MethodGet || token != stored.submissionToken {
			writeError(w, http.StatusNotFound, "request_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, RequestInstructions{
			PublicKey:     stored.publicKey,
			Fingerprint:   clientcrypto.RecipientFingerprint(stored.publicKey),
			ExpiresAt:     stored.expiresAt,
			Generation:    stored.generation,
			CanSubmit:     stored.state == "waiting",
			EncryptedNote: stored.encryptedNote,
		})
	case "submit":
		f.submitRequest(w, r, stored, token)
	case "upload":
		f.reserveRequest(w, r, stored, token)
	case "finalize":
		f.finalizeRequest(w, r, stored, token)
	case "attempt":
		var input RequestAttempt
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if token != stored.submissionToken || input.Generation != stored.generation {
			writeError(w, http.StatusNotFound, "request_unavailable")
			return
		}
		state := "waiting"
		if stored.state == "submitted" || stored.state == "consumed" {
			state = "unavailable"
			if input.AttemptToken == stored.attemptToken {
				state = "accepted"
			}
		} else if input.AttemptToken != "" && stored.reservedKey != "" {
			state = "uploading"
		}
		writeJSON(w, http.StatusOK, RequestAttemptStatus{Generation: stored.generation, State: state})
	case "owner":
		if r.Method != http.MethodGet || token != stored.retrievalToken {
			writeError(w, http.StatusNotFound, "request_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, RequestOwnerStatus{
			PublicKey:   stored.publicKey,
			Fingerprint: clientcrypto.RecipientFingerprint(stored.publicKey),
			ExpiresAt:   stored.expiresAt,
			State:       stored.state,
		})
	case "open":
		f.openRequest(w, r, stored, token)
	case "revoke":
		if token != stored.retrievalToken {
			writeError(w, http.StatusNotFound, "request_unavailable")
			return
		}
		switch stored.state {
		case "consumed":
			writeError(w, http.StatusConflict, "request_conflict")
		case "cancelled":
			writeJSON(w, http.StatusOK, map[string]string{"state": "cancelled"})
		default:
			stored.state = "cancelled"
			writeJSON(w, http.StatusOK, map[string]string{"state": "cancelled"})
		}
	default:
		writeError(w, http.StatusNotFound, "request_unavailable")
	}
}

func attemptDigest(kind string, size int64, envelope RequestEnvelope, ciphertext string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%v|%s", kind, size, envelope, ciphertext)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (f *fakeFlick) submitRequest(w http.ResponseWriter, r *http.Request, stored *storedRequest, token string) {
	if token != stored.submissionToken {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}
	var input SubmitRequestInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if input.Generation != stored.generation {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}
	digest := attemptDigest(input.Kind, input.SizeBytes, input.Envelope, input.Ciphertext)
	if stored.state == "submitted" || stored.state == "consumed" {
		if input.AttemptToken == stored.attemptToken && digest == stored.attemptDigest {
			writeJSON(w, http.StatusOK, RequestReceipt{Generation: stored.generation, State: "submitted"})
		} else {
			writeError(w, http.StatusConflict, "request_conflict")
		}
		return
	}
	if stored.state != "waiting" {
		writeError(w, http.StatusConflict, "request_conflict")
		return
	}

	body, err := base64.StdEncoding.DecodeString(input.Ciphertext)
	if err != nil || int64(len(body)) != input.SizeBytes || !strings.HasPrefix(string(body), "age-encryption.org/v1\n") {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if len(input.AttemptToken) != 43 || input.Envelope.Version != 2 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if (input.Kind == "text") != (input.Envelope.EncryptedFilename == "") {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	stored.attemptToken = input.AttemptToken
	stored.attemptDigest = digest
	stored.kind = input.Kind
	stored.envelope = input.Envelope
	stored.ciphertext = body
	stored.sizeBytes = input.SizeBytes
	stored.state = "submitted"
	writeJSON(w, http.StatusOK, RequestReceipt{Generation: stored.generation, State: "submitted"})
}

func (f *fakeFlick) reserveRequest(w http.ResponseWriter, r *http.Request, stored *storedRequest, token string) {
	if token != stored.submissionToken {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}
	var input RequestUploadInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if input.Generation != stored.generation || input.Kind != "file" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	digest := attemptDigest(input.Kind, input.SizeBytes, input.Envelope, input.CiphertextSHA256)
	if stored.state == "submitted" {
		if input.AttemptToken == stored.attemptToken && digest == stored.attemptDigest {
			writeJSON(w, http.StatusOK, RequestUploadResult{Generation: stored.generation, State: "submitted"})
		} else {
			writeError(w, http.StatusConflict, "request_conflict")
		}
		return
	}
	if stored.state != "waiting" || stored.reservedKey != "" {
		writeError(w, http.StatusConflict, "request_conflict")
		return
	}

	stored.attemptToken = input.AttemptToken
	stored.attemptDigest = digest
	stored.kind = input.Kind
	stored.envelope = input.Envelope
	stored.sizeBytes = input.SizeBytes
	stored.reservedKey = "reqobj-" + input.AttemptToken
	stored.reservedSize = input.SizeBytes
	writeJSON(w, http.StatusOK, RequestUploadResult{
		Generation:           stored.generation,
		State:                "uploading",
		Upload:               &PresignedUpload{URL: f.server.URL + "/bucket/" + stored.reservedKey, Method: http.MethodPut, ExpiresAt: time.Now().Add(15 * time.Minute).Format(time.RFC3339), Headers: map[string]string{"X-Flick-Signed": "yes"}},
		ReservationExpiresAt: time.Now().Add(15 * time.Minute).Format(time.RFC3339),
	})
}

func (f *fakeFlick) putRequestObject(w http.ResponseWriter, r *http.Request, key string) {
	if r.Header.Get("X-Flick-Signed") != "yes" {
		writeError(w, http.StatusForbidden, "signature_mismatch")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, stored := range f.requests {
		if stored.reservedKey == key {
			if int64(len(body)) != stored.reservedSize {
				writeError(w, http.StatusForbidden, "signature_mismatch")
				return
			}
			f.objects[key] = body
			stored.uploaded = true
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found")
}

func (f *fakeFlick) finalizeRequest(w http.ResponseWriter, r *http.Request, stored *storedRequest, token string) {
	if token != stored.submissionToken {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}
	var input RequestAttempt
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if stored.state == "submitted" {
		if input.AttemptToken == stored.attemptToken {
			writeJSON(w, http.StatusOK, RequestReceipt{Generation: stored.generation, State: "submitted"})
		} else {
			writeError(w, http.StatusConflict, "request_conflict")
		}
		return
	}
	if !stored.uploaded || input.AttemptToken != stored.attemptToken {
		writeError(w, http.StatusConflict, "request_conflict")
		return
	}
	stored.ciphertext = f.objects[stored.reservedKey]
	stored.state = "submitted"
	writeJSON(w, http.StatusOK, RequestReceipt{Generation: stored.generation, State: "submitted"})
}

func (f *fakeFlick) openRequest(w http.ResponseWriter, r *http.Request, stored *storedRequest, token string) {
	if token != stored.retrievalToken {
		writeError(w, http.StatusNotFound, "request_unavailable")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if stored.state != "submitted" {
		writeError(w, http.StatusConflict, "request_conflict")
		return
	}
	stored.state = "consumed"
	f.requestOpenCalls++

	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Flick-Kind", stored.kind)
	if stored.envelope.EncryptedFilename != "" {
		header.Set("Flick-Encrypted-Filename", stored.envelope.EncryptedFilename)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stored.ciphertext)
}

func TestRequestSubmitReceiveTextRoundTrip(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, err := ParseSubmissionLink(created.SubmissionLink)
	if err != nil {
		t.Fatalf("parse submission link: %v", err)
	}
	retLink, err := ParseRetrievalLink(created.RetrievalLink)
	if err != nil {
		t.Fatalf("parse retrieval link: %v", err)
	}

	submitted, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "the reply"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.State != "submitted" || submitted.Generation != 1 {
		t.Errorf("submit result = %+v", submitted)
	}

	status, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir(), StatusOnly: true})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != "submitted" || status.Opened != nil {
		t.Errorf("status = %+v", status)
	}
	if fake.requestOpenCalls != 0 {
		t.Errorf("a status check consumed the reply")
	}

	received, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if received.Opened == nil || string(received.Opened.Plaintext) != "the reply" {
		t.Fatalf("received = %+v", received.Opened)
	}
	if received.Opened.WrittenPath == "" {
		t.Error("the reply was not saved to a file")
	}

	after, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir(), StatusOnly: true})
	if err != nil {
		t.Fatalf("status after: %v", err)
	}
	if after.State != "consumed" {
		t.Errorf("state after open = %q, want consumed", after.State)
	}
}

func TestRequestNeverSendsTheIdentityOrTokensInBodies(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, _ := ParseSubmissionLink(created.SubmissionLink)
	retLink, _ := ParseRetrievalLink(created.RetrievalLink)

	if _, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "reply"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, Reveal: true}); err != nil {
		t.Fatalf("receive: %v", err)
	}

	fake.mu.Lock()
	bodies := append([]string(nil), fake.requestBodies...)
	fake.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("create called %d times", len(bodies))
	}
	for _, secret := range []string{"AGE-SECRET-KEY", subLink.Token, retLink.Token} {
		if strings.Contains(bodies[0], secret) {
			t.Errorf("the create body leaked %q", secret)
		}
	}
}

func TestRequestWithNoteRoundTrip(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL, Note: "bring the staging password"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.HasNote {
		t.Error("the note was not recorded")
	}
	subLink, err := ParseSubmissionLink(created.SubmissionLink)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if subLink.MemoIdentity == nil {
		t.Fatal("the submission link carries no memo key")
	}
	retLink, _ := ParseRetrievalLink(created.RetrievalLink)

	submitted, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "staging-password"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.Note != "bring the staging password" {
		t.Errorf("submitter note = %q", submitted.Note)
	}
	received, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(received.Opened.Plaintext) != "staging-password" {
		t.Errorf("reply = %q", received.Opened.Plaintext)
	}
}

func TestRequestCredentialAndFileRoundTrip(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, _ := ParseSubmissionLink(created.SubmissionLink)
	retLink, _ := ParseRetrievalLink(created.RetrievalLink)

	if _, err := SubmitToRequest(ctx, client, SubmitOptions{
		Link:       subLink,
		Credential: `{"v":1,"type":"login","fields":[{"id":"password","label":"Password","value":"hunter2","secret":true}]}`,
	}); err != nil {
		t.Fatalf("submit credential: %v", err)
	}
	received, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("receive credential: %v", err)
	}
	if received.Opened.Credential == nil || len(received.Opened.Credential.Fields) != 1 {
		t.Fatalf("credential reply = %+v", received.Opened.Credential)
	}
	if received.Opened.Credential.Fields[0].Value != "hunter2" {
		t.Errorf("credential value = %q", received.Opened.Credential.Fields[0].Value)
	}

	created2, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	subLink2, _ := ParseSubmissionLink(created2.SubmissionLink)
	retLink2, _ := ParseRetrievalLink(created2.RetrievalLink)
	if _, err := SubmitToRequest(ctx, client, SubmitOptions{
		Link:     subLink2,
		FileName: "report.pdf",
		File:     strings.NewReader("file payload"),
	}); err != nil {
		t.Fatalf("submit file: %v", err)
	}
	received2, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink2, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("receive file: %v", err)
	}
	if got, _ := os.ReadFile(received2.Opened.WrittenPath); string(got) != "file payload" {
		t.Errorf("file reply = %q from %s", got, received2.Opened.WrittenPath)
	}
}

func TestRequestLargeFileUsesPresignedUpload(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 512
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, _ := ParseSubmissionLink(created.SubmissionLink)
	retLink, _ := ParseRetrievalLink(created.RetrievalLink)

	payload := strings.Repeat("x", 4096)
	submitted, err := SubmitToRequest(ctx, client, SubmitOptions{
		Link:     subLink,
		FileName: "big.bin",
		File:     strings.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !submitted.Uploaded {
		t.Errorf("a %d-byte file did not take the presigned path", len(payload))
	}

	received, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got, _ := os.ReadFile(received.Opened.WrittenPath); string(got) != payload {
		t.Errorf("large round trip mismatch from %s", received.Opened.WrittenPath)
	}
}

func TestSubmitRefusesASubstitutedServerKey(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, _ := ParseSubmissionLink(created.SubmissionLink)

	replacement, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	for _, stored := range fake.requests {
		stored.publicKey = clientcrypto.RecipientStringOf(replacement)
	}
	fake.mu.Unlock()

	if _, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "reply"}); err == nil {
		t.Fatal("a substituted key was accepted")
	} else if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("err = %v, want the fingerprint mismatch", err)
	}

	fake.mu.Lock()
	state := fake.requests[created.ID].state
	fake.mu.Unlock()
	if state != "waiting" {
		t.Errorf("state = %q; nothing should have been submitted", state)
	}
}

func TestReceiveWithoutAKeyDoesNotConsumeTheReply(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	subLink, _ := ParseSubmissionLink(created.SubmissionLink)
	if _, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "reply"}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	stripped, _, _ := strings.Cut(created.RetrievalLink, "&key=")
	keyless, err := ParseRetrievalLink(stripped)
	if err != nil {
		t.Fatalf("parse keyless link: %v", err)
	}
	if keyless.Identity != nil {
		t.Fatal("the stripped link still carried a key")
	}
	if _, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: keyless, OutputDir: t.TempDir()}); err == nil {
		t.Fatal("a keyless link opened the reply")
	}
	if fake.requestOpenCalls != 0 {
		t.Errorf("the open endpoint was called %d times; the reply was burned for nothing", fake.requestOpenCalls)
	}
}

func TestRequestRevokeCancelsWithoutConsuming(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx := context.Background()

	created, err := CreateRequest(ctx, client, RequestOptions{TTL: DefaultTTL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	retLink, _ := ParseRetrievalLink(created.RetrievalLink)

	result, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, Revoke: true})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if result.State != "cancelled" {
		t.Errorf("state = %q", result.State)
	}
	if fake.requestOpenCalls != 0 {
		t.Error("revoking called the open endpoint")
	}
}
