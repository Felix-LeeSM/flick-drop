package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
)

const (
	uaChromeWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	uaSafariIPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	uaFirefoxLinux  = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0"
)

// pipeDo는 파이프 엔드포인트 하나를 호출한다. headers의 빈 값은 보내지 않는다.
func pipeDo(router http.Handler, method, path string, headers map[string]string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for name, value := range headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func pipeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

type pipeFixture struct {
	t      *testing.T
	router http.Handler
	coord  *piperelay.Coordinator
}

func newPipeFixture(t *testing.T) pipeFixture {
	t.Helper()
	coord := piperelay.NewCoordinator(piperelay.CoordinatorOptions{MaxConcurrent: 10, TTL: 5 * time.Second})
	t.Cleanup(func() { _ = coord.Close() })
	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled:    true,
		PipeCoordinator:     coord,
		AllowedOrigin:       "https://flick.example",
		OpenRatePerMinute:   100,
		CreateRatePerMinute: 100,
	})
	return pipeFixture{t: t, router: fixture.router, coord: coord}
}

func (f pipeFixture) init(requireConfirmation bool) pipeInitResponse {
	f.t.Helper()
	body := strings.NewReader("")
	if requireConfirmation {
		body = strings.NewReader(`{"require_confirmation":true}`)
	}
	rec := pipeDo(f.router, http.MethodPost, "/api/pipe/init", map[string]string{"Content-Type": "application/json"}, body)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("init: status %d: %s", rec.Code, rec.Body.String())
	}
	var out pipeInitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode init: %v", err)
	}
	return out
}

func (f pipeFixture) join(code, userAgent string) *httptest.ResponseRecorder {
	return pipeDo(f.router, http.MethodPost, "/api/pipe/join", map[string]string{pipeCodeHeader: code, "User-Agent": userAgent}, nil)
}

func (f pipeFixture) mustJoin(code, userAgent string) pipeJoinResponse {
	f.t.Helper()
	rec := f.join(code, userAgent)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("join: status %d: %s", rec.Code, rec.Body.String())
	}
	var out pipeJoinResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode join: %v", err)
	}
	return out
}

// receive는 /receive를 백그라운드로 열고 끝난 응답을 채널로 돌려준다.
func (f pipeFixture) receive(code, connectionID string) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		out <- pipeDo(f.router, http.MethodGet, "/api/pipe/receive", map[string]string{pipeCodeHeader: code, pipeConnectionHeader: connectionID}, nil)
	}()
	return out
}

// waitFor는 /wait를 버전과 함께 반복해 done이 참인 응답을 돌려준다.
func (f pipeFixture) waitFor(init pipeInitResponse, done func(pipeWaitResponse) bool) pipeWaitResponse {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var since uint64
	for time.Now().Before(deadline) {
		rec := pipeDo(f.router, http.MethodGet, "/api/pipe/wait?since="+jsonUint(since), map[string]string{pipeCodeHeader: init.Code, pipeSenderHeader: init.SenderToken}, nil)
		if rec.Code != http.StatusOK {
			f.t.Fatalf("wait: status %d: %s", rec.Code, rec.Body.String())
		}
		var out pipeWaitResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode wait: %v", err)
		}
		if done(out) {
			return out
		}
		since = out.Version
	}
	f.t.Fatal("wait condition not reached")
	return pipeWaitResponse{}
}

func jsonUint(v uint64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (f pipeFixture) senderCall(path string, init pipeInitResponse, connectionID string) *httptest.ResponseRecorder {
	return pipeDo(f.router, http.MethodPost, path, map[string]string{
		pipeCodeHeader:       init.Code,
		pipeSenderHeader:     init.SenderToken,
		pipeConnectionHeader: connectionID,
	}, nil)
}

func (f pipeFixture) send(init pipeInitResponse, payload []byte) *httptest.ResponseRecorder {
	return pipeDo(f.router, http.MethodPost, "/api/pipe/send", map[string]string{
		pipeCodeHeader:   init.Code,
		pipeSenderHeader: init.SenderToken,
		"Flick-Kind":     "text",
	}, bytes.NewReader(payload))
}

func awaitRecorder(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(3 * time.Second):
		t.Fatal("request did not finish")
		return nil
	}
}

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
	for _, prefix := range []string{"/api/pipe", "/api/v1/pipe"} {
		for _, ep := range []struct {
			method string
			path   string
		}{
			{http.MethodPost, "/init"},
			{http.MethodPost, "/join"},
			{http.MethodGet, "/receive"},
			{http.MethodGet, "/wait"},
			{http.MethodPost, "/select"},
			{http.MethodPost, "/decline"},
			{http.MethodPost, "/cancel"},
			{http.MethodPost, "/send"},
		} {
			rec := pipeDo(fixture.router, ep.method, prefix+ep.path, nil, nil)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status = %d, want 503", ep.method, prefix+ep.path, rec.Code)
			}
		}
	}
}

func TestPipeRelay_ConfirmationOffFullFlow(t *testing.T) {
	f := newPipeFixture(t)

	resp := performJSON(t, f.router, http.MethodGet, "/api/config", nil)
	var cfg configResponse
	decodeBody(t, resp, &cfg)
	if !cfg.PipeRelayEnabled {
		t.Fatalf("pipe_relay_enabled = false, want true")
	}

	init := f.init(false)
	if len(init.Code) != 6 || init.SenderToken == "" || init.RequireConfirmation {
		t.Fatalf("init = %+v", init)
	}
	saltBytes, err := base64.StdEncoding.DecodeString(init.Salt)
	if err != nil || len(saltBytes) != 16 {
		t.Fatalf("init salt invalid: %v, len = %d", err, len(saltBytes))
	}

	join := f.mustJoin(init.Code, uaChromeWindows)
	if join.VerifyCode != "" || join.Device != "" || join.RequireConfirmation {
		t.Fatalf("confirmation-off join leaked verification fields: %+v", join)
	}
	received := f.receive(init.Code, join.ConnectionID)
	f.waitFor(init, func(w pipeWaitResponse) bool { return w.Status == "ready" })

	payload := bytes.Repeat([]byte("Real-time live pipe streaming payload 1234567890\n"), 1024) // ~48 KiB
	sendReq := httptest.NewRequest(http.MethodPost, "/api/pipe/send", bytes.NewReader(payload))
	sendReq.Header.Set(pipeCodeHeader, init.Code)
	sendReq.Header.Set(pipeSenderHeader, init.SenderToken)
	sendReq.Header.Set("Flick-Kind", "file")
	sendReq.Header.Set("Flick-Encrypted-Filename", "ZmlsZW5hbWUudHh0")
	sendRec := httptest.NewRecorder()
	f.router.ServeHTTP(sendRec, sendReq)
	if sendRec.Code != http.StatusOK {
		t.Fatalf("send status = %d: %s", sendRec.Code, sendRec.Body.String())
	}

	rec := awaitRecorder(t, received)
	if rec.Code != http.StatusOK {
		t.Fatalf("receive status = %d", rec.Code)
	}
	if rec.Header().Get("X-Flick-Pipe-Salt") != init.Salt || rec.Header().Get("Flick-Kind") != "file" || rec.Header().Get("Flick-Encrypted-Filename") != "ZmlsZW5hbWUudHh0" {
		t.Fatalf("receive headers = %v", rec.Header())
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("received body len %d, want %d", rec.Body.Len(), len(payload))
	}
}

func TestPipeRelay_ConfirmationOffLateConnectorsGet409WithoutBurning(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(false)

	first := f.mustJoin(init.Code, uaChromeWindows)
	second := f.mustJoin(init.Code, uaSafariIPhone)
	received := f.receive(init.Code, first.ConnectionID)
	f.waitFor(init, func(w pipeWaitResponse) bool { return w.Status == "ready" })

	// 첫 연결이 이긴 뒤 늦게 온 연결은 몇 번이 와도 409만 받고 세션을 태우지 않는다.
	lost := awaitRecorder(t, f.receive(init.Code, second.ConnectionID))
	if lost.Code != http.StatusConflict || pipeErrorCode(t, lost) != "already_connected" {
		t.Fatalf("losing receiver: %d %s", lost.Code, lost.Body.String())
	}
	for i := 0; i < 5; i++ {
		rec := f.join(init.Code, uaFirefoxLinux)
		if rec.Code != http.StatusConflict || pipeErrorCode(t, rec) != "already_connected" {
			t.Fatalf("late join %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	if rec := f.send(init, []byte("first wins")); rec.Code != http.StatusOK {
		t.Fatalf("send after late connectors: %d %s", rec.Code, rec.Body.String())
	}
	if rec := awaitRecorder(t, received); rec.Code != http.StatusOK || rec.Body.String() != "first wins" {
		t.Fatalf("winner: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPipeRelay_WaitingRoomSelectStreamsToChosenOnly(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)
	if !init.RequireConfirmation {
		t.Fatalf("init did not record require_confirmation: %+v", init)
	}

	agents := []string{uaChromeWindows, uaSafariIPhone, uaFirefoxLinux}
	var joins []pipeJoinResponse
	var receives []<-chan *httptest.ResponseRecorder
	for _, ua := range agents {
		join := f.mustJoin(init.Code, ua)
		joins = append(joins, join)
		receives = append(receives, f.receive(init.Code, join.ConnectionID))
	}

	wait := f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == 3 })
	wantDevices := []string{"Chrome on Windows", "Safari on iPhone", "Firefox on Linux"}
	for i, conn := range wait.Connections {
		if conn.ConnectionID != joins[i].ConnectionID || conn.VerifyCode != joins[i].VerifyCode || conn.Device != wantDevices[i] || conn.Index != i+1 {
			t.Fatalf("connection %d = %+v, join = %+v", i, conn, joins[i])
		}
		if len(conn.VerifyCode) != 3 {
			t.Fatalf("verify code %q is not 3 digits", conn.VerifyCode)
		}
	}
	if joins[0].VerifyCode == joins[1].VerifyCode || joins[1].VerifyCode == joins[2].VerifyCode || joins[0].VerifyCode == joins[2].VerifyCode {
		t.Fatalf("verify codes repeat: %v %v %v", joins[0].VerifyCode, joins[1].VerifyCode, joins[2].VerifyCode)
	}
	time.Sleep(50 * time.Millisecond) // 세 /receive가 모두 붙도록

	if rec := f.senderCall("/api/pipe/select", init, joins[1].ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("select: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.send(init, []byte("for the iPhone only")); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}

	if rec := awaitRecorder(t, receives[1]); rec.Code != http.StatusOK || rec.Body.String() != "for the iPhone only" {
		t.Fatalf("chosen receiver: %d %q", rec.Code, rec.Body.String())
	}
	for _, i := range []int{0, 2} {
		rec := awaitRecorder(t, receives[i])
		if rec.Code != http.StatusGone || pipeErrorCode(t, rec) != "not_selected" {
			t.Fatalf("receiver %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestPipeRelay_DeclineFreesSlotAndBlocksThatClient(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)

	denied := f.mustJoin(init.Code, uaChromeWindows)
	deniedReceive := f.receive(init.Code, denied.ConnectionID)
	f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == 1 })

	if rec := f.senderCall("/api/pipe/decline", init, denied.ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	rec := awaitRecorder(t, deniedReceive)
	if rec.Code != http.StatusForbidden || pipeErrorCode(t, rec) != "declined" {
		t.Fatalf("declined receiver: %d %s", rec.Code, rec.Body.String())
	}
	f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == 0 })

	// 같은 IP와 User-Agent는 이 세션에 다시 들어오지 못한다. 다른 기기는 들어온다.
	rejoin := f.join(init.Code, uaChromeWindows)
	if rejoin.Code != http.StatusForbidden || pipeErrorCode(t, rejoin) != "declined" {
		t.Fatalf("rejoin from declined client: %d %s", rejoin.Code, rejoin.Body.String())
	}
	other := f.mustJoin(init.Code, uaSafariIPhone)
	if other.Device != "Safari on iPhone" {
		t.Fatalf("other join = %+v", other)
	}
	if f.coord.ActiveSessions() != 1 {
		t.Fatal("decline or blocked rejoin burned the session")
	}
}

func TestPipeRelay_RoomFullRejectsEleventhWithoutBurning(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)

	for i := 0; i < piperelay.MaxPendingConnections; i++ {
		f.mustJoin(init.Code, uaChromeWindows)
	}
	rec := f.join(init.Code, uaSafariIPhone)
	if rec.Code != http.StatusConflict || pipeErrorCode(t, rec) != "room_full" {
		t.Fatalf("11th join: %d %s", rec.Code, rec.Body.String())
	}
	wait := f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == piperelay.MaxPendingConnections })
	if wait.Status != "waiting" || f.coord.ActiveSessions() != 1 {
		t.Fatalf("session after room_full: %+v", wait)
	}
}

func TestPipeRelay_SenderCallsNeedTheSenderToken(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)
	join := f.mustJoin(init.Code, uaChromeWindows)

	forged := pipeInitResponse{Code: init.Code, SenderToken: "forged"}
	for _, path := range []string{"/api/pipe/select", "/api/pipe/decline", "/api/pipe/cancel"} {
		if rec := f.senderCall(path, forged, join.ConnectionID); rec.Code != http.StatusNotFound {
			t.Fatalf("%s with a forged token: %d", path, rec.Code)
		}
	}
	if rec := f.send(forged, []byte("x")); rec.Code != http.StatusNotFound {
		t.Fatalf("send with a forged token: %d", rec.Code)
	}
	if rec := pipeDo(f.router, http.MethodGet, "/api/pipe/wait", map[string]string{pipeCodeHeader: init.Code}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("wait without sender token: %d", rec.Code)
	}
	if f.coord.ActiveSessions() != 1 {
		t.Fatal("forged sender calls ended the session")
	}

	if rec := f.senderCall("/api/pipe/cancel", init, ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel with the real token: %d %s", rec.Code, rec.Body.String())
	}
	if f.coord.ActiveSessions() != 0 {
		t.Fatal("cancel did not remove the session")
	}
}

func TestPipeRelay_VersionedRoute(t *testing.T) {
	f := newPipeFixture(t)
	rec := pipeDo(f.router, http.MethodPost, "/api/v1/pipe/init", nil, nil)
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
	f := newPipeFixture(t)

	if rec := pipeDo(f.router, http.MethodPost, "/api/pipe/join", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing header: status = %d, want 400", rec.Code)
	}
	if rec := f.join("abcdef", uaChromeWindows); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid code: status = %d, want 400", rec.Code)
	}
	if rec := f.join("999999", uaChromeWindows); rec.Code != http.StatusNotFound {
		t.Fatalf("non-existent code: status = %d, want 404", rec.Code)
	}
	init := f.init(true)
	if rec := awaitRecorder(t, f.receive(init.Code, "guessed-connection-id")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connection id: status = %d, want 404", rec.Code)
	}
	bad := pipeDo(f.router, http.MethodPost, "/api/pipe/init", nil, strings.NewReader(`{"unknown":1}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("init with unknown field: status = %d, want 400", bad.Code)
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
	for _, header := range []string{"X-Flick-Pipe-Code", "X-Flick-Pipe-Sender", "X-Flick-Pipe-Connection", "Flick-Kind", "Flick-Encrypted-Filename"} {
		if !strings.Contains(allowHeaders, header) {
			t.Fatalf("Access-Control-Allow-Headers %q missing %s", allowHeaders, header)
		}
	}
	if exposeHeaders := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposeHeaders, "X-Flick-Pipe-Salt") {
		t.Fatalf("Access-Control-Expose-Headers %q missing X-Flick-Pipe-Salt", exposeHeaders)
	}
}

func TestPipeDeviceLabel(t *testing.T) {
	tests := map[string]string{
		uaChromeWindows: "Chrome on Windows",
		uaSafariIPhone:  "Safari on iPhone",
		uaFirefoxLinux:  "Firefox on Linux",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36":                      "Chrome on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15":                      "Safari on macOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.0.0 Mobile/15E148 Safari/604.1": "Chrome on iPhone",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36": "Samsung Internet on Android",
		"curl/8.7.1": "Unknown browser",
		"":           "Unknown browser",
	}
	for ua, want := range tests {
		if got := pipeDeviceLabel(ua); got != want {
			t.Errorf("pipeDeviceLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}
