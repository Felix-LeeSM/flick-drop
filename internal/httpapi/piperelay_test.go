package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
)

func TestPipeRelay_Disabled(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled: false,
	})

	// 1. /api/config reports false
	resp := performJSON(t, fixture.router, http.MethodGet, "/api/config", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/config: status = %d, want 200", resp.Code)
	}
	var cfg configResponse
	decodeBody(t, resp, &cfg)
	if cfg.PipeRelayEnabled {
		t.Fatalf("pipe_relay_enabled = true, want false when disabled")
	}

	// 2. All pipe endpoints return 503
	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/pipe/init"},
		{http.MethodGet, "/api/pipe/wait"},
		{http.MethodGet, "/api/pipe/receive"},
		{http.MethodPost, "/api/pipe/send"},
		{http.MethodPost, "/api/v1/pipe/init"},
		{http.MethodGet, "/api/v1/pipe/wait"},
		{http.MethodGet, "/api/v1/pipe/receive"},
		{http.MethodPost, "/api/v1/pipe/send"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		fixture.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", ep.method, ep.path, rec.Code)
		}
	}
}

func TestPipeRelay_FullFlow(t *testing.T) {
	coord := piperelay.NewCoordinator(piperelay.CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
	})
	defer coord.Close()

	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled:  true,
		PipeCoordinator:   coord,
		AllowedOrigin:     "https://flick.example",
		OpenRatePerMinute: 100,
	})

	// Verify /api/config reports enabled
	resp := performJSON(t, fixture.router, http.MethodGet, "/api/config", nil)
	var cfg configResponse
	decodeBody(t, resp, &cfg)
	if !cfg.PipeRelayEnabled {
		t.Fatalf("pipe_relay_enabled = false, want true")
	}

	// 1. Sender initializes rendezvous
	initResp := performJSON(t, fixture.router, http.MethodPost, "/api/pipe/init", nil)
	if initResp.Code != http.StatusCreated {
		t.Fatalf("POST /api/pipe/init: status = %d, want 201: %s", initResp.Code, initResp.Body.String())
	}
	var initData pipeInitResponse
	decodeBody(t, initResp, &initData)
	if len(initData.Code) != 6 {
		t.Fatalf("init code = %q, want 6 digits", initData.Code)
	}
	saltBytes, err := base64.StdEncoding.DecodeString(initData.Salt)
	if err != nil || len(saltBytes) != 16 {
		t.Fatalf("init salt invalid: %v, len = %d", err, len(saltBytes))
	}

	testPayload := bytes.Repeat([]byte("Real-time live pipe streaming payload 1234567890\n"), 1024) // ~48 KiB

	var wg sync.WaitGroup
	var waitStatus int
	var sendStatus int
	var receiveStatus int
	var receivedBody bytes.Buffer
	var receiveSaltHeader string
	var receiveKindHeader string
	var receiveFilenameHeader string

	// 2. Sender starts waiting on /api/pipe/wait
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/api/pipe/wait", nil)
		req.Header.Set("X-Flick-Pipe-Code", initData.Code)
		rec := httptest.NewRecorder()
		fixture.router.ServeHTTP(rec, req)
		waitStatus = rec.Code

		// Once wait unblocks with 200, sender streams data via /api/pipe/send
		if rec.Code == http.StatusOK {
			sendReq := httptest.NewRequest(http.MethodPost, "/api/pipe/send", bytes.NewReader(testPayload))
			sendReq.Header.Set("X-Flick-Pipe-Code", initData.Code)
			sendReq.Header.Set("Flick-Kind", "file")
			sendReq.Header.Set("Flick-Encrypted-Filename", "ZmlsZW5hbWUudHh0")
			sendRec := httptest.NewRecorder()
			fixture.router.ServeHTTP(sendRec, sendReq)
			sendStatus = sendRec.Code
		}
	}()

	// 3. Receiver connects to /api/pipe/receive
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // ensure sender wait is active
		req := httptest.NewRequest(http.MethodGet, "/api/pipe/receive", nil)
		req.Header.Set("X-Flick-Pipe-Code", initData.Code)
		rec := httptest.NewRecorder()
		fixture.router.ServeHTTP(rec, req)
		receiveStatus = rec.Code
		receiveSaltHeader = rec.Header().Get("X-Flick-Pipe-Salt")
		receiveKindHeader = rec.Header().Get("Flick-Kind")
		receiveFilenameHeader = rec.Header().Get("Flick-Encrypted-Filename")
		_, _ = io.Copy(&receivedBody, rec.Body)
	}()

	wg.Wait()

	if waitStatus != http.StatusOK {
		t.Fatalf("wait status = %d, want 200", waitStatus)
	}
	if sendStatus != http.StatusOK {
		t.Fatalf("send status = %d, want 200", sendStatus)
	}
	if receiveStatus != http.StatusOK {
		t.Fatalf("receive status = %d, want 200", receiveStatus)
	}

	if receiveSaltHeader != initData.Salt {
		t.Fatalf("receiver salt header = %q, want %q", receiveSaltHeader, initData.Salt)
	}
	if receiveKindHeader != "file" {
		t.Fatalf("receiver kind header = %q, want 'file'", receiveKindHeader)
	}
	if receiveFilenameHeader != "ZmlsZW5hbWUudHh0" {
		t.Fatalf("receiver filename header = %q, want 'ZmlsZW5hbWUudHh0'", receiveFilenameHeader)
	}
	if !bytes.Equal(receivedBody.Bytes(), testPayload) {
		t.Fatalf("received body does not match sent body: got len %d, want %d", receivedBody.Len(), len(testPayload))
	}
}

func TestPipeRelay_VersionedRoute(t *testing.T) {
	coord := piperelay.NewCoordinator(piperelay.CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
	})
	defer coord.Close()

	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled: true,
		PipeCoordinator:  coord,
	})

	// /api/v1/pipe/init works
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipe/init", nil)
	rec := httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/pipe/init status = %d, want 201", rec.Code)
	}

	var initData pipeInitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &initData); err != nil {
		t.Fatalf("decode /api/v1/pipe/init: %v", err)
	}
	if len(initData.Code) != 6 {
		t.Fatalf("code = %q, want 6 digits", initData.Code)
	}
}

func TestPipeRelay_InvalidAndMissingCodes(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled: true,
	})

	// Missing header
	req := httptest.NewRequest(http.MethodGet, "/api/pipe/wait", nil)
	rec := httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing header: status = %d, want 400", rec.Code)
	}

	// Invalid code (non-numeric)
	req = httptest.NewRequest(http.MethodGet, "/api/pipe/wait", nil)
	req.Header.Set("X-Flick-Pipe-Code", "abcdef")
	rec = httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid code: status = %d, want 400", rec.Code)
	}

	// Non-existent valid code -> 404
	req = httptest.NewRequest(http.MethodGet, "/api/pipe/receive", nil)
	req.Header.Set("X-Flick-Pipe-Code", "999999")
	rec = httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-existent code: status = %d, want 404", rec.Code)
	}
}

func TestPipeRelay_CORS(t *testing.T) {
	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled: true,
		AllowedOrigin:    "https://flick.example",
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/pipe/init", nil)
	req.Header.Set("Origin", "https://flick.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Flick-Pipe-Code")
	rec := httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	if !bytes.Contains([]byte(allowHeaders), []byte("X-Flick-Pipe-Code")) {
		t.Fatalf("Access-Control-Allow-Headers %q missing X-Flick-Pipe-Code", allowHeaders)
	}
	if !bytes.Contains([]byte(allowHeaders), []byte("Flick-Kind")) {
		t.Fatalf("Access-Control-Allow-Headers %q missing Flick-Kind", allowHeaders)
	}
	if !bytes.Contains([]byte(allowHeaders), []byte("Flick-Encrypted-Filename")) {
		t.Fatalf("Access-Control-Allow-Headers %q missing Flick-Encrypted-Filename", allowHeaders)
	}
	exposeHeaders := rec.Header().Get("Access-Control-Expose-Headers")
	if !bytes.Contains([]byte(exposeHeaders), []byte("X-Flick-Pipe-Salt")) {
		t.Fatalf("Access-Control-Expose-Headers %q missing X-Flick-Pipe-Salt", exposeHeaders)
	}
}
