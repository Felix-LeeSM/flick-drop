package piperelay

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
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
	salt []byte
	meta SessionMetadata
	err  error
}

// startReceive는 connID로 Receive를 열고 결과를 채널로 돌려준다.
func startReceive(ctx context.Context, coord *Coordinator, code, connID string) <-chan receiveResult {
	out := make(chan receiveResult, 1)
	go func() {
		pr, salt, meta, cleanup, err := coord.Receive(ctx, code, connID)
		if err != nil {
			out <- receiveResult{err: err}
			return
		}
		defer cleanup()
		body, err := io.ReadAll(pr)
		out <- receiveResult{body: body, salt: salt, meta: meta, err: err}
	}()
	return out
}

func clientKey(n byte) ClientKey {
	var k ClientKey
	k[0] = n
	return k
}

func newTestCoordinator(t *testing.T, opts CoordinatorOptions) *Coordinator {
	t.Helper()
	if opts.TTL == 0 {
		opts.TTL = 5 * time.Second
	}
	coord := NewCoordinator(opts)
	t.Cleanup(func() { _ = coord.Close() })
	return coord
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

func TestCoordinator_LifecycleConfirmationOff(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{MaxConcurrent: 10})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	init, err := coord.Init(ctx, InitOptions{})
	if err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	if len(init.Code) != 6 || len(init.Salt) != 16 || init.ExpiresIn <= 0 || init.SenderToken == "" {
		t.Fatalf("Init() invalid output: %+v", init)
	}

	join, err := coord.Join(init.Code, "Chrome on Windows", clientKey(1))
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if join.VerifyCode != "" || join.Device != "" || join.RequireConfirmation {
		t.Fatalf("confirmation-off join leaked verification fields: %+v", join)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)

	payload := bytes.Repeat([]byte("Flick live relay streaming data chunk! 1234567890\n"), 4096) // ~200 KiB
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "file", EncryptedFilename: "dGVzdC50eHQ="}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recv(t, received)
	if got.err != nil || !bytes.Equal(got.body, payload) || !bytes.Equal(got.salt, init.Salt) {
		t.Fatalf("receive mismatch: err=%v len=%d", got.err, len(got.body))
	}
	if got.meta.Kind != "file" || got.meta.EncryptedFilename != "dGVzdC50eHQ=" {
		t.Fatalf("metadata mismatch: %+v", got.meta)
	}
	if active := coord.ActiveSessions(); active != 0 {
		t.Fatalf("ActiveSessions() after completion = %d, want 0", active)
	}
}

func TestCoordinator_CapacityLimit(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{MaxConcurrent: 2})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := coord.Init(ctx, InitOptions{}); err != nil {
			t.Fatalf("Init %d failed: %v", i+1, err)
		}
	}
	if _, err := coord.Init(ctx, InitOptions{}); err != ErrCapacityExceeded {
		t.Fatalf("Init 3 expected ErrCapacityExceeded, got: %v", err)
	}
}

func TestCoordinator_TTL_Expiry(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{TTL: 50 * time.Millisecond, CleanInterval: 20 * time.Millisecond})
	ctx := context.Background()
	init, err := coord.Init(ctx, InitOptions{})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := coord.Wait(ctx, init.Code, init.SenderToken, 0, time.Second); err != ErrNotFound && err != ErrExpired {
		t.Fatalf("expected ErrNotFound or ErrExpired, got: %v", err)
	}
	if active := coord.ActiveSessions(); active != 0 {
		t.Fatalf("ActiveSessions() = %d, want 0 after TTL expiry", active)
	}
}

func TestCoordinator_SenderTokenRequired(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx := context.Background()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})
	join, _ := coord.Join(init.Code, "x", clientKey(1))

	// 6자리 코드만 아는 사람은 송신자 행동을 할 수 없고, 응답은 없는 코드와 같다.
	if _, err := coord.Wait(ctx, init.Code, "forged", 0, time.Millisecond); err != ErrNotFound {
		t.Fatalf("Wait with forged token: %v", err)
	}
	if err := coord.Select(init.Code, "", join.ConnectionID); err != ErrNotFound {
		t.Fatalf("Select without token: %v", err)
	}
	if err := coord.Cancel(init.Code, "forged"); err != ErrNotFound {
		t.Fatalf("Cancel with forged token: %v", err)
	}
	if _, err := coord.Send(ctx, init.Code, "forged", SessionMetadata{}, strings.NewReader("x")); err != ErrNotFound {
		t.Fatalf("Send with forged token: %v", err)
	}
	if coord.ActiveSessions() != 1 {
		t.Fatal("forged sender calls must not end the session")
	}
}

func TestCoordinator_VerifyCodesArePerConnectionAndDistinct(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background(), InitOptions{RequireConfirmation: true})

	seen := map[string]bool{}
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(init.Code, "Safari on iPhone", clientKey(byte(i)))
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
		if join.Device != "Safari on iPhone" || !join.RequireConfirmation {
			t.Fatalf("join result = %+v", join)
		}
	}

	snap := waitPending(t, coord, init, MaxPendingConnections)
	for i, p := range snap.Pending {
		if p.Index != i+1 || !seen[p.VerifyCode] {
			t.Fatalf("pending[%d] = %+v, want join order with issued codes", i, p)
		}
	}
}

func TestCoordinator_SelectStreamsToChosenConnectionOnly(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})

	var joins []JoinResult
	var results []<-chan receiveResult
	for i := 0; i < 3; i++ {
		join, err := coord.Join(init.Code, "d", clientKey(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		joins = append(joins, join)
		results = append(results, startReceive(ctx, coord, init.Code, join.ConnectionID))
	}
	waitPending(t, coord, init, 3)
	// 세 Receive가 모두 붙은 뒤에 고른다(전송이 끝나 세션이 사라진 뒤 붙으면 ErrNotFound를 받는다).
	time.Sleep(50 * time.Millisecond)

	if err := coord.Select(init.Code, init.SenderToken, joins[1].ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	payload := []byte("only for the chosen receiver")
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "text"}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := recv(t, results[1]); got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("chosen receiver: err=%v body=%q", got.err, got.body)
	}
	for _, i := range []int{0, 2} {
		if got := recv(t, results[i]); got.err != ErrNotSelected || len(got.body) != 0 {
			t.Fatalf("receiver %d: err=%v body=%q, want ErrNotSelected and nothing", i, got.err, got.body)
		}
	}
}

func TestCoordinator_DeclineFreesSlotWithoutBurning(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})

	var first JoinResult
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(init.Code, "d", clientKey(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		if i == 0 {
			first = join
		}
	}
	declined := startReceive(ctx, coord, init.Code, first.ConnectionID)
	waitPending(t, coord, init, MaxPendingConnections)

	if err := coord.Decline(init.Code, init.SenderToken, first.ConnectionID); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if got := recv(t, declined); got.err != ErrDeclined {
		t.Fatalf("declined receiver err = %v, want ErrDeclined", got.err)
	}
	if coord.ActiveSessions() != 1 {
		t.Fatal("decline burned the session")
	}
	if _, err := coord.Join(init.Code, "d", clientKey(100)); err != nil {
		t.Fatalf("join into the reopened slot: %v", err)
	}
	if err := coord.Decline(init.Code, init.SenderToken, first.ConnectionID); err != ErrNotPending {
		t.Fatalf("second decline of the same connection: %v", err)
	}
}

func TestCoordinator_DeclinedClientCannotRejoin(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background(), InitOptions{RequireConfirmation: true})

	denied, _ := coord.Join(init.Code, "d", clientKey(1))
	if err := coord.Decline(init.Code, init.SenderToken, denied.ConnectionID); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if _, err := coord.Join(init.Code, "d", clientKey(1)); err != ErrDeclined {
		t.Fatalf("rejoin from the declined client: err = %v, want ErrDeclined", err)
	}
	if _, err := coord.Join(init.Code, "d", clientKey(2)); err != nil {
		t.Fatalf("a different client must still join: %v", err)
	}
	if coord.ActiveSessions() != 1 {
		t.Fatal("blocked rejoin burned the session")
	}
}

func TestCoordinator_RoomFullRejectsWithoutBurning(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})

	var last JoinResult
	for i := 0; i < MaxPendingConnections; i++ {
		join, err := coord.Join(init.Code, "d", clientKey(byte(i)))
		if err != nil {
			t.Fatalf("Join %d: %v", i, err)
		}
		last = join
	}
	for i := 0; i < 5; i++ {
		if _, err := coord.Join(init.Code, "d", clientKey(byte(50+i))); err != ErrRoomFull {
			t.Fatalf("extra join %d: err = %v, want ErrRoomFull", i, err)
		}
	}
	if coord.ActiveSessions() != 1 {
		t.Fatal("room-full rejections burned the session")
	}

	// 세션은 그대로 살아 있어 전송까지 끝난다.
	received := startReceive(ctx, coord, init.Code, last.ConnectionID)
	if err := coord.Select(init.Code, init.SenderToken, last.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("ok")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != "ok" {
		t.Fatalf("receive after room-full: %+v", got)
	}
}

func TestCoordinator_ConfirmationOffFirstWinsAndLateConnectorsNeverBurn(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{})

	first, _ := coord.Join(init.Code, "d", clientKey(1))
	second, _ := coord.Join(init.Code, "d", clientKey(2))
	winner := startReceive(ctx, coord, init.Code, first.ConnectionID)
	waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })

	if got := recv(t, startReceive(ctx, coord, init.Code, second.ConnectionID)); got.err != ErrAlreadyConnected {
		t.Fatalf("second receiver: %v, want ErrAlreadyConnected", got.err)
	}
	// 예전에는 늦은 연결 3번이 세션을 태웠다. 이제 몇 번이 와도 세션과 전송은 그대로다.
	for i := 0; i < 5; i++ {
		if _, err := coord.Join(init.Code, "d", clientKey(byte(10+i))); err != ErrAlreadyConnected {
			t.Fatalf("late join %d: %v", i, err)
		}
	}
	if err := coord.Select(init.Code, init.SenderToken, second.ConnectionID); err != ErrConfirmationDisabled {
		t.Fatalf("Select with confirmation off: %v", err)
	}
	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, strings.NewReader("first wins")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, winner); string(got.body) != "first wins" {
		t.Fatalf("winner: %+v", got)
	}
}

func TestCoordinator_PendingReceiverLeavingFreesSlot(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	init, _ := coord.Init(context.Background(), InitOptions{RequireConfirmation: true})

	join, _ := coord.Join(init.Code, "d", clientKey(1))
	recvCtx, recvCancel := context.WithCancel(context.Background())
	left := startReceive(recvCtx, coord, init.Code, join.ConnectionID)
	waitPending(t, coord, init, 1)

	recvCancel()
	if got := recv(t, left); got.err == nil {
		t.Fatal("cancelled receive returned no error")
	}
	waitPending(t, coord, init, 0)
	if coord.ActiveSessions() != 1 {
		t.Fatal("a pending receiver leaving burned the session")
	}
}

func TestCoordinator_BurnsNeverCutAnActiveStream(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{TTL: 100 * time.Millisecond, CleanInterval: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})

	join, _ := coord.Join(init.Code, "d", clientKey(1))
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)
	waitPending(t, coord, init, 1)
	if err := coord.Select(init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}

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
		_, _ = coord.Join(init.Code, "d", clientKey(byte(20+i)))
	}
	if err := coord.Cancel(init.Code, init.SenderToken); err != ErrStreaming {
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

func TestCoordinator_CancelEndsWaitingReceivers(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx, InitOptions{RequireConfirmation: true})
	join, _ := coord.Join(init.Code, "d", clientKey(1))
	waiting := startReceive(ctx, coord, init.Code, join.ConnectionID)
	waitPending(t, coord, init, 1)
	// Join은 Receive보다 먼저 목록에 오르므로, Receive가 붙을 시간을 준다
	// (세션이 사라진 뒤에 붙으면 ErrNotFound를 받는다).
	time.Sleep(50 * time.Millisecond)

	if err := coord.Cancel(init.Code, init.SenderToken); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := recv(t, waiting); got.err != ErrCancelled {
		t.Fatalf("waiting receiver: %v, want ErrCancelled", got.err)
	}
	if coord.ActiveSessions() != 0 {
		t.Fatal("cancel did not remove the session")
	}
}
