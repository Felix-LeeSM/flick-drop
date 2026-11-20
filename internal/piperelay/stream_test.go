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
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

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
	payload := bytes.Repeat([]byte("cross replica ciphertext chunk 0123456789\n"), 32*1024)
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

// 레이트리미터용 클라이언트는 재시도 없이 250ms에 끊기므로 운영에서도 읽기 한 번 실패가 일어난다.
type flakyReads struct{ remaining atomic.Int32 }

func (f *flakyReads) DialHook(next redis.DialHook) redis.DialHook { return next }

func (f *flakyReads) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "hmget" && f.remaining.Add(-1) >= 0 {
			err := errors.New("injected valkey timeout")
			cmd.SetErr(err)
			return err
		}
		return next(ctx, cmd)
	}
}

func (f *flakyReads) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func flakyPod(t *testing.T, network *podNetwork) (*Coordinator, *flakyReads) {
	t.Helper()
	flaky := &flakyReads{}
	valkey := redis.NewClient(&redis.Options{Addr: network.valkey.Options().Addr})
	valkey.AddHook(flaky)
	t.Cleanup(func() { _ = valkey.Close() })
	return network.startPod(CoordinatorOptions{Valkey: valkey}), flaky
}

func selectedReceiver(t *testing.T, ctx context.Context, senderPod, receiverPod *Coordinator) (InitResult, JoinResult) {
	t.Helper()
	init, err := senderPod.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	join, err := receiverPod.Join(ctx, init.Code, client(1))
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if err := senderPod.Select(ctx, init.Code, init.SenderToken, join.ConnectionID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	return init, join
}

// 실패를 그대로 돌려주면 송신 자리는 이미 차지돼 다시 보낼 수 없다(409).
func TestStream_SenderSurvivesOneFailedReadWhileWaiting(t *testing.T) {
	network := newPodNetwork(t)
	senderPod, flaky := flakyPod(t, network)
	receiverPod := network.startPod(CoordinatorOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init, join := selectedReceiver(t, ctx, senderPod, receiverPod)
	id, _ := receiverPod.roomIDFor(init.Code)

	payload := []byte("ciphertext after a valkey hiccup")
	type sendResult struct {
		n   int64
		err error
	}
	sent := make(chan sendResult, 1)
	go func() {
		n, err := senderPod.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, bytes.NewReader(payload))
		sent <- sendResult{n, err}
	}()
	for {
		r, err := receiverPod.view(ctx, id)
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		if r.SenderClaimed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	flaky.remaining.Store(1)

	received := startReceive(ctx, receiverPod, init.Code, join.ConnectionID)

	select {
	case got := <-sent:
		if got.err != nil || got.n != int64(len(payload)) {
			t.Fatalf("Send = %d, %v; want %d bytes", got.n, got.err, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not finish")
	}
	if got := recv(t, received); got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("receiver got %q, err %v", got.body, got.err)
	}
	if flaky.remaining.Load() >= 1 {
		t.Fatal("the injected read failure never happened")
	}
}

func TestStream_ReceiverSurvivesOneFailedReadWhileWaiting(t *testing.T) {
	network := newPodNetwork(t)
	senderPod := network.startPod(CoordinatorOptions{})
	receiverPod, flaky := flakyPod(t, network)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init, join := selectedReceiver(t, ctx, senderPod, receiverPod)

	received := startReceive(ctx, receiverPod, init.Code, join.ConnectionID)
	waitUntil(t, senderPod, init, func(s WaitSnapshot) bool { return s.ReceiverReady })
	flaky.remaining.Store(1)
	deadline := time.Now().Add(2*resyncInterval + time.Second)
	for flaky.remaining.Load() >= 1 {
		if time.Now().After(deadline) {
			t.Fatal("the receiver never re-read the room")
		}
		time.Sleep(10 * time.Millisecond)
	}

	payload := []byte("ciphertext after a valkey hiccup")
	n, err := senderPod.Send(ctx, init.Code, init.SenderToken, SessionMetadata{}, bytes.NewReader(payload))

	if err != nil || n != int64(len(payload)) {
		t.Fatalf("Send = %d, %v; want %d bytes", n, err, len(payload))
	}
	if got := recv(t, received); got.err != nil || !bytes.Equal(got.body, payload) {
		t.Fatalf("receiver got %q, err %v", got.body, got.err)
	}
}
