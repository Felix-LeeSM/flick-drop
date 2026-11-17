package piperelay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 송신자와 수신자가 서로 다른 레플리카에 붙어도 암호문은 수신자 쪽 레플리카로
// 곧장 흘러가고, 파일 헤더도 그 경로로 함께 간다.
func TestStream_CrossReplicaTransferReachesTheReceiverReplica(t *testing.T) {
	network := newPodNetwork(t)
	senderPod := network.startPod(CoordinatorOptions{})
	receiverPod := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	init, err := senderPod.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	join, err := receiverPod.Join(ctx, init.Code, client(1))
	if err != nil {
		t.Fatalf("Join on the other replica: %v", err)
	}
	selected := pollUntilDecided(ctx, receiverPod, init.Code, join.ConnectionID, time.Second)
	waitPending(t, senderPod, init, 1)
	if err := senderPod.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := decided(t, selected); err != nil {
		t.Fatalf("receiver status on its replica: %v, want selected", err)
	}

	received := startReceive(ctx, receiverPod, init.Code, join.ConnectionID)
	ready := waitUntil(t, senderPod, init, func(s WaitSnapshot) bool { return s.ReceiverReady })
	if ready.Recipient != recipientOf(1) {
		t.Fatalf("ready recipient = %q", ready.Recipient)
	}
	payload := bytes.Repeat([]byte("cross replica ciphertext chunk 0123456789\n"), 32*1024) // ~1.3 MiB
	n, err := senderPod.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "file", EncryptedFilename: "ZW5jcnlwdGVk"}, bytes.NewReader(payload))
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("Send = %d, %v; want %d bytes", n, err, len(payload))
	}
	got := recv(t, received)
	if got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("receiver got %d bytes, err %v", len(got.body), got.err)
	}
	if got.meta.Kind != "file" || got.meta.EncryptedFilename != "ZW5jcnlwdGVk" {
		t.Fatalf("metadata = %+v", got.meta)
	}
	if n := active(t, receiverPod); n != 0 {
		t.Fatalf("ActiveSessions after the transfer = %d, want 0", n)
	}
}

// 롤링 업데이트로 방을 만든 레플리카가 내려가도 대기방은 Valkey에 남아, 다른
// 레플리카가 이어서 선택부터 전송까지 끝낸다.
func TestStream_RoomOutlivesTheReplicaThatCreatedIt(t *testing.T) {
	network := newPodNetwork(t)
	oldPod := network.startPod(CoordinatorOptions{})
	newPod := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	init, _ := oldPod.Init(ctx)
	join, err := oldPod.Join(ctx, init.Code, client(1))
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if err := oldPod.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if snap := waitPending(t, newPod, init, 1); snap.Pending[0].ID != join.ConnectionID {
		t.Fatalf("waiting room on the new replica = %+v", snap.Pending)
	}
	if err := newPod.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select on the new replica: %v", err)
	}
	received := startReceive(ctx, newPod, init.Code, join.ConnectionID)
	if _, err := newPod.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "text"}, strings.NewReader("after the restart")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != "after the restart" {
		t.Fatalf("receive: %+v", got)
	}
}

// Valkey에는 방 메타데이터만 있다: 스트리밍 도중에도 키 이름에 6자리 코드가 없고,
// 값 어디에도 암호문·송신자 토큰·암호화된 파일 이름·원시 클라이언트 해시가 없다.
func TestStream_ValkeyHoldsNoCiphertextCodeOrToken(t *testing.T) {
	network := newPodNetwork(t)
	coord := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	init, _ := coord.Init(ctx)
	joiner := client(7)
	join, _ := coord.Join(ctx, init.Code, joiner)
	if err := coord.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)
	waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })

	const marker = "CIPHERTEXT-MARKER-0f9e8d7c"
	body, writer := io.Pipe()
	sendDone := make(chan error, 1)
	go func() {
		_, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "file", EncryptedFilename: "RklMRU5BTUUtTUFSS0VS"}, body)
		sendDone <- err
	}()
	if _, err := writer.Write([]byte(marker)); err != nil {
		t.Fatalf("write: %v", err)
	}

	keys, err := network.valkey.Keys(ctx, network.namespace+"*").Result()
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	var dump strings.Builder
	for _, key := range keys {
		if strings.Contains(key, init.Code) {
			t.Fatalf("valkey key %q contains the 6-digit code", key)
		}
		switch kind := network.valkey.Type(ctx, key).Val(); kind {
		case "hash":
			for field, value := range network.valkey.HGetAll(ctx, key).Val() {
				fmt.Fprintf(&dump, "%s=%s\n", field, value)
			}
		case "string":
			dump.WriteString(network.valkey.Get(ctx, key).Val() + "\n")
		case "zset":
			dump.WriteString(strings.Join(network.valkey.ZRange(ctx, key, 0, -1).Val(), "\n"))
		default:
			t.Fatalf("unexpected valkey type %q for %q", kind, key)
		}
	}
	for name, secret := range map[string]string{
		"ciphertext":         marker,
		"sender token":       init.SenderToken,
		"encrypted filename": "RklMRU5BTUUtTUFSS0VS",
		"raw client key":     fmt.Sprintf("%x", joiner.Key[:]),
		"raw client network": fmt.Sprintf("%x", joiner.Network[:]),
	} {
		if strings.Contains(dump.String(), secret) {
			t.Fatalf("valkey holds the %s", name)
		}
	}

	_ = writer.Close()
	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := recv(t, received); string(got.body) != marker {
		t.Fatalf("receive: %+v", got)
	}
}

// 종료 신호를 받은 레플리카는 열려 있는 Status·Wait 롱폴을 곧바로 현재 상태로
// 돌려보내, 브라우저가 다른 레플리카로 다시 폴링하게 한다. 방은 그대로다.
func TestStream_DrainAnswersOpenLongPollsAtOnce(t *testing.T) {
	coord := newTestCoordinator(t, CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init, _ := coord.Init(ctx)
	join, _ := coord.Join(ctx, init.Code, client(1))
	since := waitPending(t, coord, init, 1).Version

	statusDone := make(chan error, 1)
	waitDone := make(chan error, 1)
	go func() {
		selected, err := coord.Status(ctx, init.Code, join.ConnectionID, 8*time.Second)
		if err == nil && selected {
			err = errors.New("selected without a pick")
		}
		statusDone <- err
	}()
	go func() {
		snap, err := coord.Wait(ctx, init.Code, init.SenderToken, since, 8*time.Second)
		if err == nil && len(snap.Pending) != 1 {
			err = fmt.Errorf("drained snapshot = %+v", snap)
		}
		waitDone <- err
	}()
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	coord.Drain()
	for name, done := range map[string]chan error{"status": statusDone, "wait": waitDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s after drain: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s still held a second after Drain", name)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain took %s", elapsed)
	}
	if active(t, coord) != 1 {
		t.Fatal("drain ended the session")
	}
}

// 스트림 리스너는 내부 토큰과 그 스트림의 1회용 비밀이 모두 맞아야 받는다. 틀린
// 요청은 기다리는 수신자를 건드리지 않고, 진짜 송신은 그 뒤에도 그대로 도착한다.
func TestStream_ListenerRequiresTokenAndStreamSecret(t *testing.T) {
	network := newPodNetwork(t)
	coord := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	init, _ := coord.Init(ctx)
	join, _ := coord.Join(ctx, init.Code, client(1))
	if err := coord.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	received := startReceive(ctx, coord, init.Code, join.ConnectionID)
	waitUntil(t, coord, init, func(s WaitSnapshot) bool { return s.ReceiverReady })
	id, _ := coord.roomIDFor(init.Code)

	listener := httptest.NewServer(coord.InternalHandler())
	defer listener.Close()
	post := func(path, token, secret string) int {
		req, _ := http.NewRequest(http.MethodPost, listener.URL+path, strings.NewReader("forged ciphertext"))
		if token != "" {
			req.Header.Set(InternalTokenHeader, token)
		}
		if secret != "" {
			req.Header.Set(StreamSecretHeader, secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for _, tc := range []struct {
		name, path, token, secret string
		want                      int
	}{
		{"no token", StreamPathPrefix + string(id), "", "s", http.StatusUnauthorized},
		{"wrong token", StreamPathPrefix + string(id), "wrong", "s", http.StatusUnauthorized},
		{"no secret", StreamPathPrefix + string(id), network.token, "", http.StatusForbidden},
		{"not a room id", StreamPathPrefix + "123456", network.token, "s", http.StatusForbidden},
		{"secret before streaming", StreamPathPrefix + string(id), network.token, "guessed", http.StatusForbidden},
		{"room with no receiver here", StreamPathPrefix + strings.Repeat("a", 64), network.token, "s", http.StatusNotFound},
	} {
		if got := post(tc.path, tc.token, tc.secret); got != tc.want {
			t.Fatalf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}

	if _, err := coord.Send(ctx, init.Code, init.SenderToken, SessionMetadata{Kind: "text"}, strings.NewReader("real ciphertext")); err != nil {
		t.Fatalf("Send after forged posts: %v", err)
	}
	if got := recv(t, received); string(got.body) != "real ciphertext" {
		t.Fatalf("receiver got %q (err %v), want only the real stream", got.body, got.err)
	}
}

// 수신자가 스트림 도중 떠나면 송신 쪽 레플리카는 io.ErrClosedPipe를 받는다(HTTP는
// 502 receiver_disconnected). 세션은 끝난다.
func TestStream_ReceiverLeavingMidStreamFailsTheSender(t *testing.T) {
	network := newPodNetwork(t)
	senderPod := network.startPod(CoordinatorOptions{})
	receiverPod := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	init, _ := senderPod.Init(ctx)
	join, _ := receiverPod.Join(ctx, init.Code, client(1))
	if err := senderPod.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	gotFirst := make(chan struct{})
	go func() {
		pr, _, cleanup, err := receiverPod.Receive(ctx, init.Code, join.ConnectionID)
		if err != nil {
			close(gotFirst)
			return
		}
		buf := make([]byte, 5)
		_, _ = io.ReadFull(pr, buf)
		close(gotFirst)
		// 수신자 브라우저가 탭을 닫았다.
		_ = pr.Close()
		cleanup()
	}()
	waitUntil(t, senderPod, init, func(s WaitSnapshot) bool { return s.ReceiverReady })

	body, writer := io.Pipe()
	sendDone := make(chan error, 1)
	go func() {
		_, err := senderPod.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, body)
		sendDone <- err
	}()
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-gotFirst
	// 수신자가 사라진 뒤에도 송신자는 계속 보낸다. 쓰기는 결국 실패하거나 버려진다.
	go func() {
		for {
			if _, err := writer.Write(bytes.Repeat([]byte("x"), 64*1024)); err != nil {
				return
			}
		}
	}()

	select {
	case err := <-sendDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Send = %v, want io.ErrClosedPipe", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not notice the receiver leaving")
	}
	_ = writer.CloseWithError(errors.New("test done"))
	if n := active(t, senderPod); n != 0 {
		t.Fatalf("ActiveSessions after a broken stream = %d, want 0", n)
	}
}
