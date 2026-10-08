package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/Felix-LeeSM/flick-drop/internal/storage"
)

type requestHTTPObjects struct {
	storage.ObjectStore
	bodies map[string][]byte
}

func (o *requestHTTPObjects) PresignPUT(_ context.Context, key string, _ int64, ttl time.Duration) (storage.UploadInstruction, error) {
	return storage.UploadInstruction{URL: "https://object.example/" + key, Method: "PUT", ExpiresAt: time.Now().Add(ttl), Headers: map[string]string{}}, nil
}
func (o *requestHTTPObjects) GetBounded(_ context.Context, key string, limit int64) ([]byte, error) {
	body, ok := o.bodies[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return bytes.Clone(body), nil
}
func (o *requestHTTPObjects) Put(_ context.Context, key string, body []byte) error {
	o.bodies[key] = bytes.Clone(body)
	return nil
}

func TestRequestLargeHTTPContractAndInternalAcknowledgement(t *testing.T) {
	ctx := context.Background()
	conn := openHTTPTestDB(t, ctx)
	objects := &requestHTTPObjects{bodies: map[string][]byte{}}
	outbox, err := events.NewOutboxStore(conn, "flick.jobs")
	if err != nil {
		t.Fatal(err)
	}
	store, err := requests.NewStore(conn, requests.Options{Objects: objects, Outbox: outbox, PayloadInlineMaxBytes: 32, MaxFileBytes: 100, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(conn, nil, Options{RequestStore: store, PayloadInlineMaxBytes: 32, OpenRatePerMinute: 100, CreateRatePerMinute: 100, InternalToken: "internal-test"})
	c := createHTTPRequest(t, router)
	path := "/api/requests/" + c.ID
	input := requestSubmission()
	var envelope requests.Envelope
	if err := json.Unmarshal(input.Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.EncryptedFilename = &requests.EncryptedFilename{Nonce: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 12)), Ciphertext: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 17))}
	raw, _ := json.Marshal(envelope)
	body := bytes.Repeat([]byte{8}, 40)
	checksum := sha256.Sum256(body)
	in := requests.UploadInput{Generation: 1, AttemptToken: input.AttemptToken, Kind: "file", SizeBytes: 24, Envelope: raw, CiphertextSHA256: base64.StdEncoding.EncodeToString(checksum[:])}
	attempt := requests.AttemptInput{Generation: 1, AttemptToken: in.AttemptToken}
	for _, endpoint := range []string{"upload", "finalize", "abandon"} {
		var payload any = attempt
		if endpoint == "upload" {
			payload = in
		}
		assertRequestStatus(t, performJSON(t, router, "POST", path+"/"+endpoint, payload, auth(c.RetrievalToken)), 404, "request_unavailable")
	}
	encoded, _ := json.Marshal(in)
	for _, invalid := range []string{`{"generation":1,` + string(encoded[1:]), strings.Replace(string(encoded), `"ciphertext_sha256":`, `"checksum":`, 1), strings.Replace(string(encoded), `"size_bytes":24`, `"size_bytes":null`, 1), string(encoded) + "{}"} {
		req := httptest.NewRequest("POST", path+"/upload", strings.NewReader(invalid))
		req.Header.Set("Authorization", "Bearer "+c.SubmissionToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertRequestStatus(t, resp, 400, "invalid_request")
	}
	tooLarge := in
	tooLarge.SizeBytes = 101
	assertRequestStatus(t, performJSON(t, router, "POST", path+"/upload", tooLarge, auth(c.SubmissionToken)), 413, "payload_too_large")
	reserved := performJSON(t, router, "POST", path+"/upload", in, auth(c.SubmissionToken))
	assertRequestStatus(t, reserved, 200, "")
	var reservation requests.UploadResult
	decodeBody(t, reserved, &reservation)
	if reservation.Upload == nil || reservation.ReservationExpiresAt == nil || reservation.State != "uploading" {
		t.Fatal("reservation shape")
	}
	var upload, final string
	if err := conn.QueryRow(`select upload_key,final_key from requests where id=?`, c.ID).Scan(&upload, &final); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reserved.Body.String(), final) {
		t.Fatal("server-only key exposed")
	}
	objects.bodies[upload] = body
	assertRequestStatus(t, performJSON(t, router, "POST", path+"/finalize", attempt, auth(c.SubmissionToken)), 200, "")
	assertRequestStatus(t, performJSON(t, router, "POST", path+"/abandon", attempt, auth(c.SubmissionToken)), 409, "request_conflict")
	opened := performJSON(t, router, "POST", path+"/open", nil, auth(c.RetrievalToken))
	assertRequestStatus(t, opened, 200, "")
	var payload requests.Payload
	decodeBody(t, opened, &payload)
	if payload.Ciphertext != base64.StdEncoding.EncodeToString(body) {
		t.Fatal("open payload changed")
	}
	if _, err := conn.Exec(`insert into request_reconciliation_pending(object_key,job_id) values(?,?)`, final, "current-job"); err != nil {
		t.Fatal(err)
	}
	ack := func(job string, headers map[string]string) *httptest.ResponseRecorder {
		return performJSON(t, router, "POST", "/internal/object-reconciliation/ack", map[string]string{"job_id": job, "object_key": final}, headers)
	}
	if ack("current-job", nil).Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated ACK")
	}
	if ack("stale-job", map[string]string{"X-Flick-Internal-Token": "internal-test"}).Code != 204 {
		t.Fatal("stale ACK not idempotent")
	}
	var pending int
	conn.QueryRow(`select count(*) from request_reconciliation_pending`).Scan(&pending)
	if pending != 1 {
		t.Fatal("stale ACK removed current claim")
	}
	for range 2 {
		if ack("current-job", map[string]string{"X-Flick-Internal-Token": "internal-test"}).Code != 204 {
			t.Fatal("terminal ACK retry")
		}
	}
	conn.QueryRow(`select count(*) from request_reconciliation_pending`).Scan(&pending)
	if pending != 0 {
		t.Fatal("ACK did not clear current claim")
	}
	disabled, err := requests.NewStore(conn, requests.Options{Outbox: outbox, PayloadInlineMaxBytes: 32, MaxFileBytes: 100, MinTTLSeconds: 300, DefaultTTLSeconds: 600, MaxTTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	disabledRouter := NewRouter(conn, nil, Options{RequestStore: disabled, PayloadInlineMaxBytes: 32, OpenRatePerMinute: 100, CreateRatePerMinute: 100})
	newRequest := createHTTPRequest(t, disabledRouter)
	assertRequestStatus(t, performJSON(t, disabledRouter, "POST", "/api/requests/"+newRequest.ID+"/upload", in, auth(newRequest.SubmissionToken)), 503, "storage_unavailable")
}
