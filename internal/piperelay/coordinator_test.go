package piperelay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"123456", "123456", false},
		{"123-456", "123456", false},
		{" 123 - 456 ", "123456", false},
		{"000000", "000000", false},
		{"999999", "999999", false},
		{"12345", "", true},   // too short
		{"1234567", "", true}, // too long
		{"123-45a", "", true}, // non-numeric
		{"", "", true},        // empty
		{"abcdef", "", true},  // letters
		{"12 34 56", "123456", false},
	}

	for _, tt := range tests {
		got, err := NormalizeCode(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("NormalizeCode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateCode(t *testing.T) {
	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() failed: %v", err)
		}
		if len(code) != 6 {
			t.Fatalf("GenerateCode() length = %d, want 6", len(code))
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("GenerateCode() non-numeric char %c in %q", c, code)
			}
		}
	}
}

// receiveResult는 Receive 한 번의 결과다: 받은 바이트 또는 오류.
type receiveResult struct {
	body []byte
	meta SessionMetadata
	err  error
}

// startReceive는 connID로 Receive를 열고 결과를 채널로 돌려준다.
func startReceive(ctx context.Context, coord *Coordinator, code, connID string) <-chan receiveResult {
	out := make(chan receiveResult, 1)
	go func() {
		pr, meta, cleanup, err := coord.Receive(ctx, code, connID)
		if err != nil {
			out <- receiveResult{err: err}
			return
		}
		defer cleanup()
		body, err := io.ReadAll(pr)
		out <- receiveResult{body: body, meta: meta, err: err}
	}()
	return out
}

func clientKey(n byte) ClientKey {
	var k ClientKey
	k[0] = n
	return k
}

func clientNetwork(n byte) ClientNetwork {
	var k ClientNetwork
	k[0] = n
	return k
}

// recipientOf는 n번 기기가 브라우저에서 만든 것으로 치는 age 수신자 문자열이다.
// 코디네이터는 모양을 검사하지 않고 저장해 송신자에게 건네기만 한다.
func recipientOf(n byte) string {
	return fmt.Sprintf("age1pq1recipient%d", n)
}

// client는 n번 기기를 나타낸다: 기기마다 클라이언트 키, 네트워크, 수신자가 따로다.
func client(n byte) JoinClient {
	return JoinClient{Device: "d", Recipient: recipientOf(n), Key: clientKey(n), Network: clientNetwork(n)}
}

// openTestValkey는 FLICK_TEST_VALKEY_ADDR의 Valkey에 붙는다. 없으면 건너뛴다
// (CI와 compose가 주소를 준다).
func openTestValkey(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("FLICK_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("FLICK_TEST_VALKEY_ADDR is not set; the live relay coordinator runs only against Valkey")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// podNetwork는 API 레플리카 여러 개가 사는 가짜 클러스터 네트워크다. 레플리카마다
// 192.0.2.N(문서용 주소) 하나를 광고하고, 스트림 전송은 그 주소:8081을 실제 테스트
// 리스너로 이어 준다. 그래서 IP 리터럴 + 고정 포트 다이얼 경로를 그대로 지난다.
type podNetwork struct {
	t         *testing.T
	valkey    *redis.Client
	namespace string
	salt      string
	token     string
	mu        sync.Mutex
	pods      int
	routes    map[string]string
}

const testInternalPort = 8081

func newPodNetwork(t *testing.T) *podNetwork {
	t.Helper()
	return &podNetwork{
		t:         t,
		valkey:    openTestValkey(t),
		namespace: "pipetest:" + randomHex(t) + ":",
		salt:      randomHex(t),
		token:     "internal-" + randomHex(t),
		routes:    map[string]string{},
	}
}

func (n *podNetwork) transport() http.RoundTripper {
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			n.mu.Lock()
			real, ok := n.routes[addr]
			n.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("no pod listens on %s", addr)
			}
			return dialer.DialContext(ctx, network, real)
		},
	}
}

// startPod는 레플리카 하나(코디네이터 + 스트림 리스너)를 띄운다.
func (n *podNetwork) startPod(opts CoordinatorOptions) *Coordinator {
	n.t.Helper()
	n.mu.Lock()
	n.pods++
	ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(n.pods)})
	n.mu.Unlock()

	if opts.TTL == 0 {
		opts.TTL = 5 * time.Second
	}
	opts.Valkey = n.valkey
	opts.Namespace = n.namespace
	opts.KeySalt = n.salt
	opts.InternalToken = n.token
	opts.AdvertiseIP = ip
	opts.InternalPort = testInternalPort
	opts.StreamTransport = n.transport()
	coord, err := NewCoordinator(opts)
	if err != nil {
		n.t.Fatalf("NewCoordinator: %v", err)
	}
	listener := httptest.NewServer(coord.InternalHandler())
	n.mu.Lock()
	n.routes[netip.AddrPortFrom(ip, testInternalPort).String()] = listener.Listener.Addr().String()
	n.mu.Unlock()
	n.t.Cleanup(func() {
		listener.Close()
		_ = coord.Close()
	})
	return coord
}

func newTestCoordinator(t *testing.T, opts CoordinatorOptions) *Coordinator {
	t.Helper()
	return newPodNetwork(t).startPod(opts)
}

// active는 모든 레플리카를 통틀어 살아 있는 세션 수다.
func active(t *testing.T, coord *Coordinator) int {
	t.Helper()
	n, err := coord.ActiveSessions(context.Background())
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	return n
}

// waitUntil은 송신자 Wait를 반복해 done이 참인 스냅숏을 돌려준다.
func waitUntil(t *testing.T, coord *Coordinator, init InitResult, done func(WaitSnapshot) bool) WaitSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var since uint64
	for {
		snap, err := coord.Wait(ctx, init.Code, init.SenderToken, since, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if done(snap) {
			return snap
		}
		since = snap.Version
	}
}

func waitPending(t *testing.T, coord *Coordinator, init InitResult, n int) WaitSnapshot {
	t.Helper()
	return waitUntil(t, coord, init, func(s WaitSnapshot) bool { return len(s.Pending) == n })
}

func recv(t *testing.T, ch <-chan receiveResult) receiveResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("receive did not finish")
		return receiveResult{}
	}
}

// pollUntilDecided는 대기 중인 수신자처럼 Status를 maxWait씩 계속 다시 연다.
// 선택되면 nil, 거절·미선택·세션 종료면 그 오류를 채널로 돌려준다.
func pollUntilDecided(ctx context.Context, coord *Coordinator, code, connID string, maxWait time.Duration) <-chan error {
	out := make(chan error, 1)
	go func() {
		for {
			selected, err := coord.Status(ctx, code, connID, maxWait)
			if err != nil || selected {
				out <- err
				return
			}
		}
	}()
	return out
}

func decided(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("status poll did not settle")
		return nil
	}
}

func TestCoordinator_Lifecycle(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{MaxConcurrent: 10})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	init, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	if len(init.Code) != 6 || init.ExpiresIn <= 0 || init.SenderToken == "" {
		t.Fatalf("Init() invalid output: %+v", init)
	}

	if snap := waitUntil(t, coord, init, func(WaitSnapshot) bool { return true }); snap.ReceiverReady || snap.Recipient != "" || len(snap.Pending) != 0 {
		t.Fatalf("snapshot before any receiver = %+v, want empty waiting room", snap)
	}
	join, err := coord.Join(context.Background(), init.Code, JoinClient{Device: "Chrome on Windows", Recipient: recipientOf(1), Key: clientKey(1)})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(join.VerifyCode) != 3 || join.Device != "Chrome on Windows" {
		t.Fatalf("unexpected join metadata: %+v", join)
	}
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)

	ready := waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })
	if ready.Recipient != recipientOf(1) {
		t.Fatalf("ready snapshot recipient = %q, want the selected receiver's %q", ready.Recipient, recipientOf(1))
	}

	payload := bytes.Repeat([]byte("Flick live relay streaming data chunk! 1234567890\n"), 4096) // ~200 KiB
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "file", EncryptedFilename: "dGVzdC50eHQ="}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recv(t, received)
	if got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("receive mismatch: err=%v len=%d", got.err, len(got.body))
	}
	if got.meta.Kind != "file" || got.meta.EncryptedFilename != "dGVzdC50eHQ=" {
		t.Fatalf("metadata mismatch: %+v", got.meta)
	}
	if n := active(t, coord); n != 0 {
		t.Fatalf("ActiveSessions() after completion = %d, want 0", n)
	}
}

func TestCoordinator_CapacityLimit(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{MaxConcurrent: 2})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := coord.Init(ctx); err != nil {
			t.Fatalf("Init %d failed: %v", i+1, err)
		}
	}
	if _, err := coord.Init(ctx); err != ErrCapacityExceeded {
		t.Fatalf("Init 3 expected ErrCapacityExceeded, got: %v", err)
	}
}

func TestCoordinator_TTL_Expiry(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{TTL: 50 * time.Millisecond, CleanInterval: 20 * time.Millisecond})
	ctx := context.Background()
	init, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := coord.Wait(ctx, init.Code, init.SenderToken, 0, time.Second); err != ErrNotFound && err != ErrExpired {
		t.Fatalf("expected ErrNotFound or ErrExpired, got: %v", err)
	}
	if n := active(t, coord); n != 0 {
		t.Fatalf("ActiveSessions() = %d, want 0 after TTL expiry", n)
	}
}

func TestCoordinator_JoinAndSelectAfterExpiryAreRejected(t *testing.T) {
	// 청소기가 돌기 전(만료 직후)에도 창이 닫힌 세션에는 들어가거나 고를 수 없다.
	coord := newTestCoordinator(t, CoordinatorOptions{TTL: 50 * time.Millisecond, CleanInterval: time.Hour})
	init, _ := coord.Init(context.Background())
	join, err := coord.Join(context.Background(), init.Code, client(1))
	if err != nil {
		t.Fatalf("Join before expiry: %v", err)
	}

	time.Sleep(80 * time.Millisecond)
	if _, err := coord.Join(context.Background(), init.Code, client(2)); err != ErrExpired {
		t.Fatalf("Join after expiry: %v, want ErrExpired", err)
	}
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != ErrExpired {
		t.Fatalf("Select after expiry: %v, want ErrExpired", err)
	}
}

func TestCoordinator_SenderTokenRequired(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx := context.Background()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(context.Background(), init.Code, client(1))

	// 6자리 코드만 아는 사람은 송신자 행동을 할 수 없고, 응답은 없는 코드와 같다.
	if _, err := coord.Wait(ctx, init.Code, "forged", 0, time.Millisecond); err != ErrNotFound {
		t.Fatalf("Wait with forged token: %v", err)
	}
	if err := coord.Select(context.Background(), init.Code, "", join.ConnectionID); err != ErrNotFound {
		t.Fatalf("Select without token: %v", err)
	}
	if err := coord.Cancel(context.Background(), init.Code, "forged"); err != ErrNotFound {
		t.Fatalf("Cancel with forged token: %v", err)
	}
	if _, err := coord.Send(ctx, init.Code, "forged", SessionMetadata{}, strings.NewReader("x")); err != ErrNotFound {
		t.Fatalf("Send with forged token: %v", err)
	}
	if active(t, coord) != 1 {
		t.Fatal("forged sender calls must not end the session")
	}
}

func TestCoordinator_VerifyCodesArePerConnectionAndDistinct(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background())

	seen := map[string]bool{}
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(context.Background(), init.Code, JoinClient{Device: "Safari on iPhone", Recipient: recipientOf(byte(i)), Key: clientKey(byte(i)), Network: clientNetwork(byte(i))})
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		if len(join.VerifyCode) != 3 || strings.Trim(join.VerifyCode, "0123456789") != "" {
			t.Fatalf("verify code %q is not 3 digits", join.VerifyCode)
		}
		if seen[join.VerifyCode] {
			t.Fatalf("verify code %q repeated within one session", join.VerifyCode)
		}
		seen[join.VerifyCode] = true
		if join.Device != "Safari on iPhone" {
			t.Fatalf("join result = %+v", join)
		}
	}

	snap := waitPending(t, coord, init, MaxPendingConnections)
	for i, p := range snap.Pending {
		if p.Index != i+1 || !seen[p.VerifyCode] {
			t.Fatalf("pending[%d] = %+v, want join order with issued codes", i, p)
		}
	}
	if snap.Recipient != "" {
		t.Fatalf("snapshot recipient %q before any receiver attached", snap.Recipient)
	}
}

func TestCoordinator_SelectStreamsToChosenConnectionOnly(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)

	var joins []JoinResult
	var polls []<-chan error
	for i := 0; i < 3; i++ {
		join, err := coord.Join(context.Background(), init.Code, client(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		joins = append(joins, join)
		polls = append(polls, pollUntilDecided(ctx, coord, init.Code, join.ConnectionID, 200*time.Millisecond))
	}
	pending := waitPending(t, coord, init, 3)
	for i, p := range pending.Pending {
		if p.ID != joins[i].ConnectionID || p.Recipient != recipientOf(byte(i)) {
			t.Fatalf("pending[%d] = %+v, want its own recipient %q", i, p, recipientOf(byte(i)))
		}
	}

	if err := coord.Select(context.Background(), init.Code, init.SenderToken, joins[1].ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := decided(t, polls[1]); err != nil {
		t.Fatalf("chosen receiver status: %v, want selected", err)
	}
	for _, i := range []int{0, 2} {
		if err := decided(t, polls[i]); err != ErrNotSelected {
			t.Fatalf("receiver %d status: %v, want ErrNotSelected", i, err)
		}
		// 고르지 않은 연결은 /receive를 열어도 아무것도 받지 못한다.
		if got := recv(t, startReceive(ctx, coord, init.Code, joins[i].ConnectionID)); got.err != ErrNotSelected || len(got.body) != 0 {
			t.Fatalf("receiver %d: err=%v body=%q, want ErrNotSelected and nothing", i, got.err, got.body)
		}
	}

	received := startReceive(ctx, coord, init.Code, joins[1].ConnectionID)
	payload := []byte("only for the chosen receiver")
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "text"}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("chosen receiver: err=%v body=%q", got.err, got.body)
	}
}

func TestCoordinator_ReceiveBeforeSelectionIsRejected(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx := context.Background()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(context.Background(), init.Code, client(1))

	// 대기 중인 연결은 /receive를 붙잡아 둘 수 없다: 선택될 때까지 Status로 기다린다.
	if got := recv(t, startReceive(ctx, coord, init.Code, join.ConnectionID)); got.err != ErrAwaitingDecision {
		t.Fatalf("Receive before Select: %v, want ErrAwaitingDecision", got.err)
	}
	if snap := waitPending(t, coord, init, 1); snap.Pending[0].ID != join.ConnectionID {
		t.Fatalf("waiting connection left the room: %+v", snap)
	}
}

func TestCoordinator_PollingReceiverOutlastsTheProxyIdleLimit(t *testing.T) {
	// 시간 축을 줄인 재현: Cloudflare 100초 대신 presence 100ms, Status 한 번은 30ms만 붙든다.
	// 수신자가 presence 시간의 다섯 배 동안 Status만 반복해도 목록에 남아 선택된다.
	coord := newTestCoordinator(t, CoordinatorOptions{PresenceTimeout: 100 * time.Millisecond, CleanInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(context.Background(), init.Code, client(1))
	poll := pollUntilDecided(ctx, coord, init.Code, join.ConnectionID, 30*time.Millisecond)

	time.Sleep(500 * time.Millisecond)
	if snap := waitPending(t, coord, init, 1); snap.Pending[0].ID != join.ConnectionID {
		t.Fatalf("polling receiver dropped: %+v", snap)
	}
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select after a long wait: %v", err)
	}
	if err := decided(t, poll); err != nil {
		t.Fatalf("status after Select: %v, want selected", err)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("late but fine")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != "late but fine" {
		t.Fatalf("receive: %+v", got)
	}
}

func TestCoordinator_JoinedConnectionThatNeverPollsIsDroppedAndFreesItsSlot(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{PresenceTimeout: 100 * time.Millisecond, CleanInterval: time.Hour})
	init, _ := coord.Init(context.Background())

	var ghosts []JoinResult
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(context.Background(), init.Code, client(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		ghosts = append(ghosts, join)
	}
	if _, err := coord.Join(context.Background(), init.Code, client(50)); err != ErrRoomFull {
		t.Fatalf("join into a full room: %v, want ErrRoomFull", err)
	}

	// 아무도 Status를 열지 않았으니 presence 시간이 지나면 자리가 비고, 세션은 그대로다.
	time.Sleep(150 * time.Millisecond)
	fresh, err := coord.Join(context.Background(), init.Code, client(51))
	if err != nil {
		t.Fatalf("join after the ghosts timed out: %v", err)
	}
	snap := waitPending(t, coord, init, 1)
	if snap.Pending[0].ID != fresh.ConnectionID {
		t.Fatalf("room after pruning = %+v, want only the fresh joiner", snap.Pending)
	}
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, ghosts[0].ConnectionID); err != ErrNotPending {
		t.Fatalf("Select a dropped connection: %v, want ErrNotPending", err)
	}
	// 떨어진 연결은 거절이 아니다: 같은 기기가 다시 들어올 수 있다.
	if _, err := coord.Join(context.Background(), init.Code, client(0)); err != nil {
		t.Fatalf("a dropped client rejoining: %v", err)
	}
	if active(t, coord) != 1 {
		t.Fatal("dropping idle connections burned the session")
	}
}

func TestCoordinator_ChosenConnectionThatNeverReceivesEndsTheSession(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{PresenceTimeout: 50 * time.Millisecond, CleanInterval: 10 * time.Millisecond})
	init, _ := coord.Init(context.Background())
	join, _ := coord.Join(context.Background(), init.Code, client(1))
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for active(t, coord) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active(t, coord) != 0 {
		t.Fatal("a chosen connection that never opened Receive kept the session alive")
	}
}

func TestCoordinator_SendEndsAtThePresenceDeadlineWhenTheChosenNeverReceives(t *testing.T) {
	// 청소기는 사실상 돌지 않는다(1시간). Send 쪽 대기가 직접 35초(여기서는 100ms) 기한을 지켜야 한다.
	for _, sendFirst := range []bool{false, true} {
		coord := newTestCoordinator(t, CoordinatorOptions{PresenceTimeout: 100 * time.Millisecond, CleanInterval: time.Hour})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		init, _ := coord.Init(ctx)
		join, _ := coord.Join(context.Background(), init.Code, client(1))

		sendDone := make(chan error, 1)
		start := time.Now()
		send := func() {
			go func() {
				_, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("x"))
				sendDone <- err
			}()
		}
		if sendFirst {
			// Select보다 먼저 기다리기 시작한 Send도 선택 뒤의 기한을 다시 읽는다.
			send()
			time.Sleep(30 * time.Millisecond)
		}
		if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
			t.Fatalf("Select: %v", err)
		}
		if !sendFirst {
			send()
		}

		select {
		case err := <-sendDone:
			if err != ErrSessionClosed {
				t.Fatalf("sendFirst=%v: Send = %v, want ErrSessionClosed", sendFirst, err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("sendFirst=%v: Send ended after %v", sendFirst, elapsed)
			}
		case <-time.After(time.Second):
			t.Fatalf("sendFirst=%v: Send still waiting a second after the 100ms presence deadline", sendFirst)
		}
		if active(t, coord) != 0 {
			t.Fatalf("sendFirst=%v: session survived its abandoned pick", sendFirst)
		}
		cancel()
	}
}

func TestCoordinator_StatusPollsPerConnectionAreCapped(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(context.Background(), init.Code, client(1))

	held := make(chan error, MaxStatusPollsPerConnection)
	for i := 0; i < MaxStatusPollsPerConnection; i++ {
		go func() {
			_, err := coord.Status(ctx, init.Code, join.ConnectionID, 3*time.Second)
			held <- err
		}()
	}
	time.Sleep(50 * time.Millisecond)

	// 한 연결 ID로 롱폴을 더 쌓을 수 없다. 거절은 연결도 세션도 건드리지 않는다.
	if _, err := coord.Status(ctx, init.Code, join.ConnectionID, time.Second); err != ErrPollInProgress {
		t.Fatalf("status poll over the cap: %v, want ErrPollInProgress", err)
	}
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	for i := 0; i < MaxStatusPollsPerConnection; i++ {
		if err := decided(t, held); err != nil {
			t.Fatalf("held poll %d: %v, want selected", i, err)
		}
	}
	if selected, err := coord.Status(ctx, init.Code, join.ConnectionID, time.Second); err != nil || !selected {
		t.Fatalf("status after the polls closed: %v %v", selected, err)
	}
}

func TestCoordinator_PerNetworkCapHolds(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background())
	sameNetwork := func(n byte) JoinClient {
		return JoinClient{Device: "d", Key: clientKey(n), Network: clientNetwork(7)}
	}

	var first JoinResult
	for i := 0; i < MaxPendingPerNetwork; i++ {
		join, err := coord.Join(context.Background(), init.Code, sameNetwork(byte(i)))
		if err != nil {
			t.Fatalf("Join %d from one network: %v", i, err)
		}
		if i == 0 {
			first = join
		}
	}
	if _, err := coord.Join(context.Background(), init.Code, sameNetwork(10)); err != ErrNetworkFull {
		t.Fatalf("join over the per-network cap: %v, want ErrNetworkFull", err)
	}
	if _, err := coord.Join(context.Background(), init.Code, client(20)); err != nil {
		t.Fatalf("join from another network: %v", err)
	}
	// 같은 네트워크의 한 자리가 비면 그 네트워크가 다시 들어온다. 세션은 그대로다.
	if err := coord.Decline(context.Background(), init.Code, init.SenderToken, first.ConnectionID); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if _, err := coord.Join(context.Background(), init.Code, sameNetwork(11)); err != nil {
		t.Fatalf("join after a slot on that network reopened: %v", err)
	}
	if active(t, coord) != 1 {
		t.Fatal("per-network rejections burned the session")
	}
}

func TestCoordinator_DeclineFreesSlotWithoutBurning(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)

	var first JoinResult
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(context.Background(), init.Code, client(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		if i == 0 {
			first = join
		}
	}
	declined := pollUntilDecided(ctx, coord, init.Code, first.ConnectionID, 200*time.Millisecond)
	waitPending(t, coord, init, MaxPendingConnections)

	if err := coord.Decline(context.Background(), init.Code, init.SenderToken, first.ConnectionID); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if err := decided(t, declined); err != ErrDeclined {
		t.Fatalf("declined receiver status = %v, want ErrDeclined", err)
	}
	if active(t, coord) != 1 {
		t.Fatal("decline burned the session")
	}
	if _, err := coord.Join(context.Background(), init.Code, client(100)); err != nil {
		t.Fatalf("join into the reopened slot: %v", err)
	}
	if err := coord.Decline(context.Background(), init.Code, init.SenderToken, first.ConnectionID); err != ErrNotPending {
		t.Fatalf("second decline of the same connection: %v", err)
	}
}

func TestCoordinator_DeclinedClientCannotRejoin(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background())

	denied, _ := coord.Join(context.Background(), init.Code, client(1))
	if err := coord.Decline(context.Background(), init.Code, init.SenderToken, denied.ConnectionID); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if _, err := coord.Join(context.Background(), init.Code, client(1)); err != ErrDeclined {
		t.Fatalf("rejoin from the declined client: err = %v, want ErrDeclined", err)
	}
	if _, err := coord.Join(context.Background(), init.Code, client(2)); err != nil {
		t.Fatalf("a different client must still join: %v", err)
	}
	if active(t, coord) != 1 {
		t.Fatal("blocked rejoin burned the session")
	}
}

func TestCoordinator_RoomFullRejectsWithoutBurning(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)

	var last JoinResult
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(context.Background(), init.Code, client(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		last = join
	}
	for i := 0; i < 5; i++ {
		if _, err := coord.Join(context.Background(), init.Code, client(byte(50+i))); err != ErrRoomFull {
			t.Fatalf("extra join %d: err = %v, want ErrRoomFull", i, err)
		}
	}
	if active(t, coord) != 1 {
		t.Fatal("room-full rejections burned the session")
	}

	// 세션은 그대로 살아 있어 전송까지 끝난다.
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, last.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	received := startReceive(ctx, coord, init.Code, last.ConnectionID)
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("ok")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != "ok" {
		t.Fatalf("receive after room-full: %+v", got)
	}
}

func TestCoordinator_SelectedLocksOutLateJoinersWithoutBurning(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)

	first, _ := coord.Join(context.Background(), init.Code, client(1))
	second, _ := coord.Join(context.Background(), init.Code, client(2))
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, first.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	winner := startReceive(ctx, coord, init.Code, first.ConnectionID)
	waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })

	if got := recv(t, startReceive(ctx, coord, init.Code, second.ConnectionID)); got.err != ErrNotSelected {
		t.Fatalf("second receiver: %v, want ErrNotSelected", got.err)
	}
	// 예전에는 늦은 연결 3번이 세션을 태웠다. 이제 몇 번이 와도 세션과 전송은 그대로다.
	for i := 0; i < 5; i++ {
		if _, err := coord.Join(context.Background(), init.Code, client(byte(10+i))); err != ErrAlreadyConnected {
			t.Fatalf("late join %d: %v", i, err)
		}
	}
	if _, err := coord.Status(ctx, init.Code, second.ConnectionID, time.Millisecond); err != ErrNotSelected {
		t.Fatalf("Status of the losing connection: %v, want ErrNotSelected", err)
	}
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("chosen wins")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, winner); string(got.body) != "chosen wins" {
		t.Fatalf("winner: %+v", got)
	}
}

func TestCoordinator_PendingReceiverStoppingPollsFreesSlot(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{PresenceTimeout: 50 * time.Millisecond, CleanInterval: 10 * time.Millisecond})
	init, _ := coord.Init(context.Background())

	join, _ := coord.Join(context.Background(), init.Code, client(1))
	pollCtx, stopPolling := context.WithCancel(context.Background())
	left := pollUntilDecided(pollCtx, coord, init.Code, join.ConnectionID, 20*time.Millisecond)
	waitPending(t, coord, init, 1)

	stopPolling()
	if err := decided(t, left); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped poll returned %v", err)
	}
	waitPending(t, coord, init, 0)
	if active(t, coord) != 1 {
		t.Fatal("a pending receiver leaving burned the session")
	}
}

func TestCoordinator_BurnsNeverCutAnActiveStream(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{TTL: 100 * time.Millisecond, CleanInterval: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)

	join, _ := coord.Join(context.Background(), init.Code, client(1))
	if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)

	body, writer := io.Pipe()
	sendDone := make(chan error, 1)
	go func() {
		_, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "text"}, body)
		sendDone <- err
	}()
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}

	// 스트리밍 도중: TTL(100ms)이 지나 청소기가 여러 번 돌고, 늦은 연결과 취소가 들어온다.
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 5; i++ {
		_, _ = coord.Join(context.Background(), init.Code, client(byte(20+i)))
	}
	if err := coord.Cancel(context.Background(), init.Code, init.SenderToken); err != ErrStreaming {
		t.Fatalf("Cancel during streaming: %v, want ErrStreaming", err)
	}

	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatalf("write second chunk: %v", err)
	}
	_ = writer.Close()
	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != "firstsecond" {
		t.Fatalf("stream was cut: %q (err %v)", got.body, got.err)
	}
}

func TestCoordinator_CancelRacingSendNeverCutsAStream(t *testing.T) {
	// Cancel과 Send가 동시에 오면 둘 중 하나만 이긴다: 취소가 이기면 스트림은 시작조차 하지
	// 않고, 스트림이 먼저 시작하면 취소는 ErrStreaming이고 수신자는 전부 받는다.
	coord := newTestCoordinator(t, CoordinatorOptions{MaxConcurrent: 1000})
	payload := bytes.Repeat([]byte("x"), 256*1024)
	cancelWon, streamWon := 0, 0
	for i := 0; i < 400; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		init, _ := coord.Init(ctx)
		join, _ := coord.Join(context.Background(), init.Code, client(1))
		if err := coord.Select(context.Background(), init.Code, init.SenderToken, join.ConnectionID); err != nil {
			t.Fatalf("iteration %d: Select: %v", i, err)
		}
		received := startReceive(ctx, coord, init.Code, join.ConnectionID)
		waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })

		sendDone := make(chan error, 1)
		go func() {
			_, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, bytes.NewReader(payload))
			sendDone <- err
		}()
		// Cancel이 Send의 여러 지점(송신 점유 전·스트리밍 표시 직전·복사 중)에 걸리도록
		// 0~3.9ms 사이로 늦춘다. Send는 Valkey 왕복이 여럿이라 늦추지 않으면 취소만 이긴다.
		time.Sleep(time.Duration(i%40) * 100 * time.Microsecond)
		cancelErr := coord.Cancel(context.Background(), init.Code, init.SenderToken)
		sendErr := <-sendDone
		got := recv(t, received)
		cancel()

		switch {
		case cancelErr == nil:
			cancelWon++
			// Send는 시작 전에 취소를 본다(세션이 이미 지워졌으면 ErrNotFound).
			if (sendErr != ErrCancelled && sendErr != ErrNotFound) || len(got.body) != 0 {
				t.Fatalf("iteration %d: cancel won but send=%v and receiver got %d bytes", i, sendErr, len(got.body))
			}
		case cancelErr == ErrStreaming:
			streamWon++
			if sendErr != nil || !bytes.Equal(got.body, payload) {
				t.Fatalf("iteration %d: stream started but was cut: send=%v received %d of %d bytes", i, sendErr, len(got.body), len(payload))
			}
		case cancelErr == ErrNotFound:
			// 스트림이 Cancel보다 먼저 끝나 세션이 이미 사라졌다.
			streamWon++
			if sendErr != nil || !bytes.Equal(got.body, payload) {
				t.Fatalf("iteration %d: finished stream incomplete: send=%v received %d bytes", i, sendErr, len(got.body))
			}
		default:
			t.Fatalf("iteration %d: Cancel returned %v", i, cancelErr)
		}
	}
	t.Logf("cancel won %d times, stream won %d times", cancelWon, streamWon)
	if cancelWon == 0 || streamWon == 0 {
		t.Fatalf("the race never reached one side (cancel won %d, stream won %d); widen the delays", cancelWon, streamWon)
	}
}

func TestCoordinator_CancelEndsWaitingReceivers(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(context.Background(), init.Code, client(1))
	waiting := pollUntilDecided(ctx, coord, init.Code, join.ConnectionID, 200*time.Millisecond)
	waitPending(t, coord, init, 1)
	// Join은 Status보다 먼저 목록에 오르므로, Status가 열릴 시간을 준다
	// (세션이 사라진 뒤에 열면 ErrNotFound를 받는다).
	time.Sleep(50 * time.Millisecond)

	if err := coord.Cancel(context.Background(), init.Code, init.SenderToken); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := decided(t, waiting); err != ErrCancelled {
		t.Fatalf("waiting receiver: %v, want ErrCancelled", err)
	}
	if active(t, coord) != 0 {
		t.Fatal("cancel did not remove the session")
	}
}
