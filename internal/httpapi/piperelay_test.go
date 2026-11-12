package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

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
	return newPipeFixtureWith(t, piperelay.CoordinatorOptions{MaxConcurrent: 10, TTL: 5 * time.Second}, 100)
}

func newPipeFixtureWith(t *testing.T, opts piperelay.CoordinatorOptions, openRatePerMinute int) pipeFixture {
	t.Helper()
	coord := piperelay.NewCoordinator(opts)
	t.Cleanup(func() { _ = coord.Close() })
	fixture := newTestRouterFixture(t, Options{
		PipeRelayEnabled:    true,
		PipeCoordinator:     coord,
		AllowedOrigin:       "https://flick.example",
		OpenRatePerMinute:   openRatePerMinute,
		CreateRatePerMinute: 100,
	})
	return pipeFixture{t: t, router: fixture.router, coord: coord}
}

// pipeTestRecipient는 userAgent 기기가 브라우저에서 만든 것으로 치는 진짜 하이브리드 age 수신자다.
// 기기(User-Agent)마다 하나씩 만들어 두고 재사용하며, 기기마다 다르다.
func pipeTestRecipient(userAgent string) string {
	pipeTestRecipientsMu.Lock()
	defer pipeTestRecipientsMu.Unlock()
	if r, ok := pipeTestRecipients[userAgent]; ok {
		return r
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		panic(err)
	}
	r := identity.Recipient().String()
	pipeTestRecipients[userAgent] = r
	return r
}

var (
	pipeTestRecipientsMu sync.Mutex
	pipeTestRecipients   = map[string]string{}
)

func pipeJoinBody(userAgent string) io.Reader {
	return strings.NewReader(`{"recipient":"` + pipeTestRecipient(userAgent) + `"}`)
}

// joinFrom는 remoteAddr(예: "[2001:db8::1]:443")에서 /join을 부른다.
func (f pipeFixture) joinFrom(path, code, userAgent, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, pipeJoinBody(userAgent))
	req.RemoteAddr = remoteAddr
	req.Header.Set(pipeCodeHeader, code)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// joinAddr는 i번째 기기마다 서로 다른 IPv4 주소를 준다(네트워크별 상한에 걸리지 않게).
func joinAddr(i int) string {
	return "198.51.100." + strconv.Itoa(i+1) + ":443"
}

// status는 /status 롱폴을 백그라운드로 한 번 열고 응답을 채널로 돌려준다.
func (f pipeFixture) status(code, connectionID string) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		out <- pipeDo(f.router, http.MethodGet, "/api/pipe/status", map[string]string{pipeCodeHeader: code, pipeConnectionHeader: connectionID}, nil)
	}()
	return out
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
	return pipeDo(f.router, http.MethodPost, "/api/pipe/join", map[string]string{pipeCodeHeader: code, "User-Agent": userAgent}, pipeJoinBody(userAgent))
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
			{http.MethodGet, "/status"},
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

	join := f.mustJoin(init.Code, uaChromeWindows)
	if join.VerifyCode != "" || join.Device != "" || join.RequireConfirmation {
		t.Fatalf("confirmation-off join leaked verification fields: %+v", join)
	}
	if w := f.waitFor(init, func(pipeWaitResponse) bool { return true }); w.Status != "waiting" || w.Recipient != "" {
		t.Fatalf("wait before any receiver attached = %+v, want no recipient", w)
	}
	received := f.receive(init.Code, join.ConnectionID)
	// 확인 없는 모드에서 송신자는 ready 응답의 recipient로 누구에게 암호화할지 안다.
	ready := f.waitFor(init, func(w pipeWaitResponse) bool { return w.Status == "ready" })
	if ready.Recipient != pipeTestRecipient(uaChromeWindows) {
		t.Fatalf("ready recipient = %q, want the attached receiver's", ready.Recipient)
	}

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
	if rec.Header().Get("X-Flick-Pipe-Salt") != "" || rec.Header().Get("Flick-Kind") != "file" || rec.Header().Get("Flick-Encrypted-Filename") != "ZmlsZW5hbWUudHh0" {
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
	var statuses []<-chan *httptest.ResponseRecorder
	for _, ua := range agents {
		rec := f.join(init.Code, ua)
		if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "age1pq1") {
			t.Fatalf("join must not echo any recipient back to a receiver: %d %s", rec.Code, rec.Body.String())
		}
		var join pipeJoinResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &join); err != nil {
			t.Fatalf("decode join: %v", err)
		}
		joins = append(joins, join)
		statuses = append(statuses, f.status(init.Code, join.ConnectionID))
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
		if conn.Recipient != pipeTestRecipient(agents[i]) {
			t.Fatalf("connection %d recipient is not the one that device sent", i)
		}
	}
	if joins[0].VerifyCode == joins[1].VerifyCode || joins[1].VerifyCode == joins[2].VerifyCode || joins[0].VerifyCode == joins[2].VerifyCode {
		t.Fatalf("verify codes repeat: %v %v %v", joins[0].VerifyCode, joins[1].VerifyCode, joins[2].VerifyCode)
	}
	// 아직 고르지 않은 연결은 /receive를 열어 둘 수 없다(프록시가 끊는 무응답 대기를 막는다).
	early := awaitRecorder(t, f.receive(init.Code, joins[0].ConnectionID))
	if early.Code != http.StatusConflict || pipeErrorCode(t, early) != "awaiting_decision" {
		t.Fatalf("receive before select: %d %s", early.Code, early.Body.String())
	}

	if rec := f.senderCall("/api/pipe/select", init, joins[1].ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("select: %d %s", rec.Code, rec.Body.String())
	}
	chosen := awaitRecorder(t, statuses[1])
	var chosenStatus pipeStatusResponse
	if err := json.Unmarshal(chosen.Body.Bytes(), &chosenStatus); err != nil || chosen.Code != http.StatusOK || chosenStatus.Status != "selected" {
		t.Fatalf("chosen status: %d %s", chosen.Code, chosen.Body.String())
	}
	for _, i := range []int{0, 2} {
		rec := awaitRecorder(t, statuses[i])
		if rec.Code != http.StatusGone || pipeErrorCode(t, rec) != "not_selected" {
			t.Fatalf("receiver %d status: %d %s", i, rec.Code, rec.Body.String())
		}
		if rec := awaitRecorder(t, f.receive(init.Code, joins[i].ConnectionID)); rec.Code != http.StatusGone || pipeErrorCode(t, rec) != "not_selected" {
			t.Fatalf("receiver %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	received := f.receive(init.Code, joins[1].ConnectionID)
	ready := f.waitFor(init, func(w pipeWaitResponse) bool { return w.Status == "ready" })
	if ready.Recipient != pipeTestRecipient(uaSafariIPhone) {
		t.Fatal("ready recipient is not the selected device's")
	}
	if rec := f.send(init, []byte("for the iPhone only")); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	if rec := awaitRecorder(t, received); rec.Code != http.StatusOK || rec.Body.String() != "for the iPhone only" {
		t.Fatalf("chosen receiver: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPipeRelay_SelectOfAConnectionThatLeftIs409NotPending(t *testing.T) {
	f := newPipeFixtureWith(t, piperelay.CoordinatorOptions{TTL: 5 * time.Second, PresenceTimeout: 50 * time.Millisecond}, 100)
	init := f.init(true)
	left := f.mustJoin(init.Code, uaChromeWindows)

	// /status를 한 번도 열지 않은 연결은 presence 시간이 지나면 목록에서 빠진다.
	time.Sleep(100 * time.Millisecond)
	f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == 0 })
	for _, connectionID := range []string{left.ConnectionID, "never-joined"} {
		for _, path := range []string{"/api/pipe/select", "/api/pipe/decline"} {
			rec := f.senderCall(path, init, connectionID)
			if rec.Code != http.StatusConflict || pipeErrorCode(t, rec) != "not_pending" {
				t.Fatalf("%s of a connection that is not waiting: %d %s", path, rec.Code, rec.Body.String())
			}
		}
	}
	if f.coord.ActiveSessions() != 1 {
		t.Fatal("a dropped connection burned the session")
	}
}

func TestPipeRelay_PendingCapPerNetworkTreatsAnIPv6Slash64AsOneNetwork(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)

	// 같은 /64 안의 서로 다른 주소 세 개, User-Agent도 서로 다르다.
	agents := []string{uaChromeWindows, uaSafariIPhone, uaFirefoxLinux}
	var first pipeJoinResponse
	for i, ua := range agents {
		rec := f.joinFrom("/api/pipe/join", init.Code, ua, "[2001:db8:1:2::"+strconv.Itoa(i+1)+"]:443")
		if rec.Code != http.StatusCreated {
			t.Fatalf("join %d from the /64: %d %s", i, rec.Code, rec.Body.String())
		}
		if i == 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &first)
		}
	}
	over := f.joinFrom("/api/pipe/join", init.Code, "curl/8", "[2001:db8:1:2:ffff::9]:443")
	if over.Code != http.StatusConflict || pipeErrorCode(t, over) != "network_full" {
		t.Fatalf("4th join from the same /64: %d %s", over.Code, over.Body.String())
	}
	if rec := f.joinFrom("/api/pipe/join", init.Code, "curl/8", "[2001:db8:1:3::1]:443"); rec.Code != http.StatusCreated {
		t.Fatalf("join from the next /64: %d %s", rec.Code, rec.Body.String())
	}

	// 거절 차단도 /64 단위다: 같은 /64의 다른 주소로 바꿔도 같은 브라우저는 다시 못 들어온다.
	if rec := f.senderCall("/api/pipe/decline", init, first.ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	rejoin := f.joinFrom("/api/pipe/join", init.Code, uaChromeWindows, "[2001:db8:1:2::abcd]:443")
	if rejoin.Code != http.StatusForbidden || pipeErrorCode(t, rejoin) != "declined" {
		t.Fatalf("rejoin from another address in the declined /64: %d %s", rejoin.Code, rejoin.Body.String())
	}
}

func TestPipeRelay_StatusCapsConcurrentPollsAndRateLimitsPerNetwork(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)
	join := f.mustJoin(init.Code, uaChromeWindows)

	// 한 연결 ID의 동시 롱폴은 piperelay.MaxStatusPollsPerConnection(2)개까지다.
	var held []<-chan *httptest.ResponseRecorder
	for i := 0; i < piperelay.MaxStatusPollsPerConnection; i++ {
		held = append(held, f.status(init.Code, join.ConnectionID))
	}
	time.Sleep(50 * time.Millisecond)
	over := awaitRecorder(t, f.status(init.Code, join.ConnectionID))
	if over.Code != http.StatusConflict || pipeErrorCode(t, over) != "poll_in_progress" {
		t.Fatalf("third concurrent status: %d %s", over.Code, over.Body.String())
	}
	if rec := f.senderCall("/api/pipe/select", init, join.ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("select: %d %s", rec.Code, rec.Body.String())
	}
	for i, ch := range held {
		if rec := awaitRecorder(t, ch); rec.Code != http.StatusOK {
			t.Fatalf("held status %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	// 네트워크(IPv6 /64)마다 분당 pipeStatusRatePerMinute번이고, /api/v1 경로도 같은 버킷을 쓴다.
	statusFrom := func(path, remoteAddr string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set(pipeCodeHeader, "123456")
		req.Header.Set(pipeConnectionHeader, "x")
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < pipeStatusRatePerMinute; i++ {
		if code := statusFrom("/api/pipe/status", "[2001:db8:5::"+strconv.Itoa(i%9+1)+"]:443"); code == http.StatusTooManyRequests {
			t.Fatalf("status %d inside the per-network budget was rate limited", i)
		}
	}
	if code := statusFrom("/api/v1/pipe/status", "[2001:db8:5::99]:443"); code != http.StatusTooManyRequests {
		t.Fatalf("status over the per-network budget: %d, want 429", code)
	}
	if code := statusFrom("/api/pipe/status", "[2001:db8:6::1]:443"); code == http.StatusTooManyRequests {
		t.Fatal("another /64 shared the status bucket")
	}
}

func TestPipeRelay_JoinRateLimitSharesOneBucketPerIPv6Slash64AndAcrossPrefixes(t *testing.T) {
	f := newPipeFixtureWith(t, piperelay.CoordinatorOptions{}, 2)

	// 버킷 2개: 같은 /64의 두 주소가 다 쓰면, 셋째 주소도, /api/v1 경로도 막힌다.
	for i, addr := range []string{"[2001:db8::1]:443", "[2001:db8::2]:443"} {
		if rec := f.joinFrom("/api/pipe/join", "123456", uaChromeWindows, addr); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("join %d inside the burst was rate limited", i)
		}
	}
	for _, path := range []string{"/api/pipe/join", "/api/v1/pipe/join"} {
		if rec := f.joinFrom(path, "123456", uaChromeWindows, "[2001:db8::3]:443"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s from a third address in the same /64: %d, want 429", path, rec.Code)
		}
	}
	if rec := f.joinFrom("/api/pipe/join", "123456", uaChromeWindows, "[2001:db8:0:1::1]:443"); rec.Code == http.StatusTooManyRequests {
		t.Fatal("another /64 shared the bucket")
	}
	// IPv4는 주소마다 그대로 따로다.
	for _, addr := range []string{"203.0.113.1:443", "203.0.113.1:443", "203.0.113.2:443"} {
		if rec := f.joinFrom("/api/pipe/join", "123456", uaChromeWindows, addr); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("IPv4 %s rate limited inside its own burst", addr)
		}
	}
}

func TestPipeRelay_DeclineFreesSlotAndBlocksThatClient(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)

	denied := f.mustJoin(init.Code, uaChromeWindows)
	deniedStatus := f.status(init.Code, denied.ConnectionID)
	f.waitFor(init, func(w pipeWaitResponse) bool { return len(w.Connections) == 1 })

	if rec := f.senderCall("/api/pipe/decline", init, denied.ConnectionID); rec.Code != http.StatusOK {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body.String())
	}
	rec := awaitRecorder(t, deniedStatus)
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
		if rec := f.joinFrom("/api/pipe/join", init.Code, uaChromeWindows, joinAddr(i)); rec.Code != http.StatusCreated {
			t.Fatalf("join %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := f.joinFrom("/api/pipe/join", init.Code, uaSafariIPhone, joinAddr(50))
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

func TestPipeRelay_JoinRequiresAValidRecipient(t *testing.T) {
	f := newPipeFixture(t)
	init := f.init(true)

	valid := pipeTestRecipient("")
	bodies := map[string]string{
		"no body":          "",
		"no recipient":     `{}`,
		"empty recipient":  `{"recipient":""}`,
		"wrong prefix":     `{"recipient":"age1qqqqqqqq"}`,
		"scrypt-like":      `{"recipient":"AGE-SECRET-KEY-PQ-1QQQQ"}`,
		"uppercase":        `{"recipient":"AGE1PQ1QQQQQQQQ"}`,
		"non-bech32 b":     `{"recipient":"age1pq1qqqbqqq"}`,
		"non-bech32 i":     `{"recipient":"age1pq1qqqiqqq"}`,
		"non-bech32 o":     `{"recipient":"age1pq1qqqoqqq"}`,
		"non-bech32 1":     `{"recipient":"age1pq1qqq1qqq"}`,
		"prefix only":      `{"recipient":"age1pq1"}`,
		"over 4096 chars":  `{"recipient":"age1pq1` + strings.Repeat("q", 4096) + `"}`,
		"bad checksum":     `{"recipient":"` + valid[:len(valid)-1] + flipBech32(valid[len(valid)-1:]) + `"}`,
		"right shape only": `{"recipient":"age1pq1` + strings.Repeat("q", 1952) + `"}`,
		"unknown field":    `{"recipient":"` + valid + `","passphrase":"x"}`,
		"not json":         `age1pq1qqqq`,
		"recipient number": `{"recipient":1}`,
	}
	for name, body := range bodies {
		rec := pipeDo(f.router, http.MethodPost, "/api/pipe/join", map[string]string{pipeCodeHeader: init.Code, "User-Agent": uaChromeWindows}, strings.NewReader(body))
		if rec.Code != http.StatusBadRequest || pipeErrorCode(t, rec) != "invalid_recipient" {
			t.Fatalf("%s: %d %s, want 400 invalid_recipient", name, rec.Code, rec.Body.String())
		}
	}
	// 잘못된 join은 대기실에 아무것도 남기지 않고, 진짜 수신자는 들어온다.
	if w := f.waitFor(init, func(pipeWaitResponse) bool { return true }); len(w.Connections) != 0 {
		t.Fatalf("rejected joins left connections: %+v", w.Connections)
	}
	if rec := pipeDo(f.router, http.MethodPost, "/api/pipe/join", map[string]string{pipeCodeHeader: init.Code}, strings.NewReader(`{"recipient":"`+valid+`"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("real recipient: %d %s", rec.Code, rec.Body.String())
	}
}

// flipBech32는 bech32 문자 하나를 다른 문자로 바꿔 체크섬을 깨뜨린다.
func flipBech32(c string) string {
	if c == "q" {
		return "p"
	}
	return "q"
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
	if exposeHeaders := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposeHeaders, "Flick-Encrypted-Filename") {
		t.Fatalf("Access-Control-Expose-Headers %q missing Flick-Encrypted-Filename", exposeHeaders)
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
