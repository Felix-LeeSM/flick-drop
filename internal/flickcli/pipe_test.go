package flickcli

import (
	"bytes"
	"context"
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

// An in-memory twin of the pipe relay: rooms keyed by a 6-digit code, wake-on-
// change long polls, select/decline/leave/cancel, and a rendezvous where /send
// and /receive block for each other like the real pod-to-pod stream.

type fakePipeConn struct {
	id        string
	index     int
	verify    string
	device    string
	recipient string
}

type fakePipeRoom struct {
	mu           sync.Mutex
	code         string
	senderToken  string
	version      int
	conns        map[string]*fakePipeConn
	order        []string
	chosenID     string
	state        string // open | closed | cancelled
	attached     bool
	body         []byte
	kind         string
	encFilename  string
	receiveCalls int
	// Knobs: the stream fails before it carries a byte, as on a relay error.
	receiveFails bool
	sendFails    bool

	cancelCalled bool
	leaveCalled  bool
	changed      chan struct{}
}

func (room *fakePipeRoom) bumpLocked() {
	room.version++
	close(room.changed)
	room.changed = make(chan struct{})
}

func (f *fakeFlick) newPipeRoom() *fakePipeRoom {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pipeCodeCounter++
	room := &fakePipeRoom{
		code:        fmt.Sprintf("%06d", 100000+f.pipeCodeCounter),
		senderToken: randomToken(),
		conns:       map[string]*fakePipeConn{},
		state:       "open",
		changed:     make(chan struct{}),
	}
	f.pipeRooms[room.code] = room
	return room
}

// failLongPoll answers whether this long poll is one the test made fail, as a
// replica stopping mid-request would.
func (f *fakeFlick) failLongPoll(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "status":
		if f.statusFailures > 0 {
			f.statusFailures--
			return true
		}
	case "wait":
		if f.waitFailures > 0 {
			f.waitFailures--
			return true
		}
	}
	return false
}

func (f *fakeFlick) routePipe(w http.ResponseWriter, r *http.Request, action string) {
	if f.failLongPoll(action) {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	code := r.Header.Get(pipeCodeHeader)
	f.mu.Lock()
	room := f.pipeRooms[code]
	f.mu.Unlock()

	switch action {
	case "init":
		f.pipeInit(w, r)
	case "join":
		f.pipeJoin(w, r, room)
	case "status":
		f.pipeStatus(w, r, room, code)
	case "wait":
		f.pipeWait(w, r, room, code)
	case "select", "decline":
		f.pipeSenderAction(w, r, room, code, action)
	case "leave":
		f.pipeLeave(w, r, room, code)
	case "cancel":
		f.pipeCancel(w, r, room, code)
	case "send":
		f.pipeSend(w, r, room, code)
	case "receive":
		f.pipeReceive(w, r, room, code)
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (f *fakeFlick) pipeInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "bad_request")
		return
	}
	room := f.newPipeRoom()
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":         room.code,
		"expires_in":   180,
		"sender_token": room.senderToken,
	})
}

func pipeRoomUnavailable(w http.ResponseWriter, room *fakePipeRoom) bool {
	if room == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return true
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.state != "open" {
		writeError(w, http.StatusGone, room.state)
		return true
	}
	return false
}

func (f *fakeFlick) pipeJoin(w http.ResponseWriter, r *http.Request, room *fakePipeRoom) {
	var input struct {
		Recipient string `json:"recipient"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 5120)).Decode(&input); err != nil || len(input.Recipient) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_recipient")
		return
	}
	if pipeRoomUnavailable(w, room) {
		return
	}

	room.mu.Lock()
	defer room.mu.Unlock()
	if len(room.conns) >= 10 {
		writeError(w, http.StatusConflict, "room_full")
		return
	}
	verify := fmt.Sprintf("%03d", 100+len(room.order))
	for {
		taken := false
		for _, conn := range room.conns {
			if conn.verify == verify {
				taken = true
			}
		}
		if !taken {
			break
		}
		verify = fmt.Sprintf("%03d", (verifyInt(verify)*7+13)%1000)
	}
	conn := &fakePipeConn{
		id:        randomToken(),
		index:     len(room.order) + 1,
		verify:    verify,
		device:    "CLI test device",
		recipient: input.Recipient,
	}
	room.conns[conn.id] = conn
	room.order = append(room.order, conn.id)
	room.bumpLocked()
	writeJSON(w, http.StatusCreated, map[string]any{
		"connection_id": conn.id,
		"verify_code":   conn.verify,
		"device":        conn.device,
		"expires_in":    180,
	})
}

func verifyInt(v string) int { return int(v[0]-'0')*100 + int(v[1]-'0')*10 + int(v[2]-'0') }

// The room view a /wait response carries; call holding room.mu.
func pipeWaitSnapshot(room *fakePipeRoom) map[string]any {
	resp := map[string]any{"status": "waiting", "version": room.version, "connections": []any{}}
	for _, id := range room.order {
		if conn := room.conns[id]; conn != nil && room.chosenID == "" {
			resp["connections"] = append(resp["connections"].([]any), map[string]any{
				"connection_id":     conn.id,
				"index":             conn.index,
				"verify_code":       conn.verify,
				"device":            conn.device,
				"recipient":         conn.recipient,
				"connected_seconds": conn.index * 7,
			})
		}
	}
	if room.chosenID != "" && room.attached {
		resp["status"] = "ready"
		resp["recipient"] = room.conns[room.chosenID].recipient
	}
	return resp
}

func (f *fakeFlick) pipeStatus(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if pipeRoomUnavailable(w, room) {
		return
	}
	connID := r.Header.Get(pipeConnHeader)

	room.mu.Lock()
	// Long-poll: hold until the connection is chosen (or dropped) so the
	// receiver does not hot-loop, like the server's ten-second hold.
	for {
		if room.state != "open" {
			room.mu.Unlock()
			writeError(w, http.StatusGone, room.state)
			return
		}
		conn := room.conns[connID]
		if conn == nil {
			room.mu.Unlock()
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if room.chosenID == connID {
			room.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"status": "selected"})
			return
		}
		if room.chosenID != "" {
			room.mu.Unlock()
			writeError(w, http.StatusGone, "not_selected")
			return
		}
		changed := room.changed
		room.mu.Unlock()
		select {
		case <-changed:
			room.mu.Lock()
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			// The real server answers each poll when its hold expires, with the state it sees then.
			if room.state != "open" {
				state := room.state
				room.mu.Unlock()
				writeError(w, http.StatusGone, state)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "waiting"})
			return
		}
	}
}

func pipeSenderAuthorized(w http.ResponseWriter, r *http.Request, room *fakePipeRoom) bool {
	// A wrong sender token answers 404 like an unknown code, never a code oracle.
	if r.Header.Get(pipeSenderHeader) != room.senderToken {
		writeError(w, http.StatusNotFound, "not_found")
		return false
	}
	return true
}

func (f *fakeFlick) pipeWait(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if pipeRoomUnavailable(w, room) {
		return
	}
	if !pipeSenderAuthorized(w, r, room) {
		return
	}
	var since int
	fmt.Sscanf(r.URL.Query().Get("since"), "%d", &since)

	room.mu.Lock()
	for {
		if room.state != "open" {
			room.mu.Unlock()
			writeError(w, http.StatusGone, room.state)
			return
		}
		if room.version != since {
			resp := pipeWaitSnapshot(room)
			room.mu.Unlock()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		changed := room.changed
		room.mu.Unlock()
		select {
		case <-changed:
			room.mu.Lock()
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			// The real server answers the hold with the current room view.
			room.mu.Lock()
			if room.state != "open" {
				state := room.state
				room.mu.Unlock()
				writeError(w, http.StatusGone, state)
				return
			}
			resp := pipeWaitSnapshot(room)
			room.mu.Unlock()
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
}

func (f *fakeFlick) pipeSenderAction(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code, action string) {
	if pipeRoomUnavailable(w, room) {
		return
	}
	if !pipeSenderAuthorized(w, r, room) {
		return
	}
	connID := r.Header.Get(pipeConnHeader)

	room.mu.Lock()
	defer room.mu.Unlock()
	if room.conns[connID] == nil {
		writeError(w, http.StatusConflict, "not_pending")
		return
	}
	if action == "select" {
		room.chosenID = connID
	} else {
		delete(room.conns, connID)
	}
	room.bumpLocked()
	status := "selected"
	if action == "decline" {
		status = "declined"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (f *fakeFlick) pipeLeave(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if room == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	connID := r.Header.Get(pipeConnHeader)

	room.mu.Lock()
	defer room.mu.Unlock()
	if room.conns[connID] == nil {
		writeError(w, http.StatusConflict, "not_pending")
		return
	}
	delete(room.conns, connID)
	room.leaveCalled = true
	room.bumpLocked()
	writeJSON(w, http.StatusOK, map[string]string{"status": "left"})
}

func (f *fakeFlick) pipeCancel(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if room == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !pipeSenderAuthorized(w, r, room) {
		return
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	room.state = "cancelled"
	room.cancelCalled = true
	room.bumpLocked()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (f *fakeFlick) pipeSend(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if pipeRoomUnavailable(w, room) {
		return
	}
	if !pipeSenderAuthorized(w, r, room) {
		return
	}

	room.mu.Lock()
	sendFails := room.sendFails
	room.mu.Unlock()
	if sendFails {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}

	room.mu.Lock()
	kind := r.Header.Get("Flick-Kind")
	if kind == "" {
		kind = "text"
	}
	room.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "transfer_failed")
		return
	}

	room.mu.Lock()
	room.kind = kind
	room.encFilename = r.Header.Get("Flick-Encrypted-Filename")
	room.body = body
	room.bumpLocked()
	// The real relay streams to the receiver's pod; the rendezvous is the part
	// the client must tolerate.
	for !room.attached {
		changed := room.changed
		room.mu.Unlock()
		select {
		case <-changed:
			room.mu.Lock()
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			room.mu.Lock()
		}
	}
	room.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "bytes_transferred": len(body)})
}

func (f *fakeFlick) pipeReceive(w http.ResponseWriter, r *http.Request, room *fakePipeRoom, code string) {
	if pipeRoomUnavailable(w, room) {
		return
	}
	connID := r.Header.Get(pipeConnHeader)

	room.mu.Lock()
	if room.chosenID == "" || room.chosenID != connID {
		room.mu.Unlock()
		writeError(w, http.StatusConflict, "awaiting_decision")
		return
	}
	if room.receiveFails {
		room.mu.Unlock()
		writeError(w, http.StatusGone, "cancelled")
		return
	}
	room.attached = true
	room.receiveCalls++
	room.bumpLocked()
	for room.body == nil {
		if room.state != "open" {
			room.mu.Unlock()
			writeError(w, http.StatusGone, room.state)
			return
		}
		changed := room.changed
		room.mu.Unlock()
		select {
		case <-changed:
			room.mu.Lock()
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			room.mu.Lock()
		}
	}
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Flick-Kind", room.kind)
	if room.encFilename != "" {
		header.Set("Flick-Encrypted-Filename", room.encFilename)
	}
	body := room.body
	room.state = "closed"
	room.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func TestLiveTextBidirectionalRoundTrip(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	receiverDone := make(chan OpenResult, 1)
	receiverErr := make(chan error, 1)
	go func() {
		var code string
		for i := 0; i < 500 && code == ""; i++ {
			fake.mu.Lock()
			for _, room := range fake.pipeRooms {
				code = room.code
			}
			fake.mu.Unlock()
			if code == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if code == "" {
			receiverErr <- errors.New("no room appeared")
			return
		}
		result, err := LiveJoin(ctx, client, LiveJoinOptions{
			Code:      code,
			OutputDir: t.TempDir(),
			Connected: func(string, int) error { return nil },
		})
		receiverDone <- result
		receiverErr <- err
	}()

	sent, err := LiveSend(ctx, client, LiveSendOptions{
		Text:    "live payload",
		Started: func(PipeSession) error { return nil },
	})
	if err != nil {
		t.Fatalf("live send: %v", err)
	}
	if sent.BytesTransferred == 0 || sent.VerifyCode == "" || sent.ConnectionID == "" {
		t.Errorf("send result = %+v", sent)
	}

	var received OpenResult
	select {
	case received = <-receiverDone:
		if err := <-receiverErr; err != nil {
			t.Fatalf("live join: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the receiver never finished")
	}
	if string(received.Plaintext) != "live payload" {
		t.Errorf("received %q", received.Plaintext)
	}
	if received.WrittenPath == "" {
		t.Error("the live payload was not saved to a reserved file")
	}
}

func TestLiveFileAndCredentialRoundTrip(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runPair := func(sendOpts LiveSendOptions, check func(t *testing.T, result OpenResult)) {
		t.Helper()
		receiverDone := make(chan OpenResult, 1)
		receiverErr := make(chan error, 1)
		go func() {
			var code string
			for i := 0; i < 500 && code == ""; i++ {
				fake.mu.Lock()
				for _, room := range fake.pipeRooms {
					if room.state == "open" {
						code = room.code
					}
				}
				fake.mu.Unlock()
				if code == "" {
					time.Sleep(10 * time.Millisecond)
				}
			}
			result, err := LiveJoin(ctx, client, LiveJoinOptions{
				Code:      code,
				OutputDir: t.TempDir(),
				Connected: func(string, int) error { return nil },
			})
			receiverDone <- result
			receiverErr <- err
		}()

		sent, err := LiveSend(ctx, client, sendOpts)
		if err != nil {
			t.Fatalf("live send: %v", err)
		}
		if sent.BytesTransferred == 0 {
			t.Fatal("nothing was transferred")
		}
		select {
		case result := <-receiverDone:
			if err := <-receiverErr; err != nil {
				t.Fatalf("live join: %v", err)
			}
			check(t, result)
		case <-time.After(30 * time.Second):
			t.Fatal("the receiver never finished")
		}
	}

	runPair(LiveSendOptions{
		FileName: "notes.txt",
		File:     strings.NewReader("file over live"),
		Started:  func(PipeSession) error { return nil },
	}, func(t *testing.T, result OpenResult) {
		if filepath.Base(result.WrittenPath) != "notes.txt" {
			t.Errorf("written = %q, want the decrypted name", result.WrittenPath)
		}
		if got, _ := os.ReadFile(result.WrittenPath); string(got) != "file over live" {
			t.Errorf("file contents = %q", got)
		}
	})

	runPair(LiveSendOptions{
		Credential: `{"v":1,"type":"login","fields":[{"id":"password","label":"Password","value":"hunter2","secret":true}]}`,
		Started:    func(PipeSession) error { return nil },
	}, func(t *testing.T, result OpenResult) {
		if result.Credential == nil || len(result.Credential.Fields) != 1 || result.Credential.Fields[0].Value != "hunter2" {
			t.Errorf("credential = %+v", result.Credential)
		}
	})
}

func TestLiveSenderCancelWhileWaitingCancelsTheRoom(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx, cancel := context.WithCancel(context.Background())

	_, err := LiveSend(ctx, client, LiveSendOptions{
		Text: "never arrives",
		Started: func(session PipeSession) error {
			cancel()
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	fake.mu.Lock()
	var room *fakePipeRoom
	for _, candidate := range fake.pipeRooms {
		room = candidate
	}
	fake.mu.Unlock()
	if room == nil {
		t.Fatal("no room was created")
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if !room.cancelCalled {
		t.Error("the waiting room was not cancelled behind the sender")
	}
}

func TestLiveReceiverLeaveWhileWaiting(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	room := fake.newPipeRoom()
	ctx, cancel := context.WithCancel(context.Background())

	_, err := LiveJoin(ctx, client, LiveJoinOptions{
		Code: room.code,
		Connected: func(string, int) error {
			cancel()
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	room.mu.Lock()
	defer room.mu.Unlock()
	if !room.leaveCalled {
		t.Error("the receiver did not leave the waiting room behind itself")
	}
}

func TestPipeWrongSenderTokenIsNotFound(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	room := fake.newPipeRoom()

	_, err := client.PipeWait(context.Background(), room.code, "not-the-token", 0)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 APIError", err)
	}
}

func TestNormalizePipeCode(t *testing.T) {
	for raw, want := range map[string]string{
		"305817":    "305817",
		" 305-817 ": "305817",
		"305 817":   "305817",
	} {
		got, err := NormalizePipeCode(raw)
		if err != nil || got != want {
			t.Errorf("NormalizePipeCode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "30581", "3058177", "30581a", "abc"} {
		if _, err := NormalizePipeCode(raw); err == nil {
			t.Errorf("NormalizePipeCode(%q) was accepted", raw)
		}
	}
}

func TestLiveSendRefusesOverLimitTextBeforeCreatingARoom(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 512
	client := fake.client()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := LiveSend(ctx, client, LiveSendOptions{
		Text:    strings.Repeat("x", 4000),
		Started: func(PipeSession) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "inline limit") {
		t.Fatalf("err = %v, want the inline-limit refusal", err)
	}

	fake.mu.Lock()
	rooms := len(fake.pipeRooms)
	fake.mu.Unlock()
	if rooms != 0 {
		t.Errorf("a room was created for an undeliverable payload: %d rooms", rooms)
	}
}

func TestLiveJoinSurfacesAnOverLimitPayloadWithItsSavedFile(t *testing.T) {
	fake := newFakeFlick(t)
	fake.inlineMax = 512
	client := fake.client()
	room := fake.newPipeRoom()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	joined := make(chan error, 1)
	var result OpenResult
	go func() {
		var err error
		result, err = LiveJoin(ctx, client, LiveJoinOptions{
			Code:      room.code,
			OutputDir: t.TempDir(),
			Connected: func(string, int) error { return nil },
		})
		joined <- err
	}()

	// Wait for the receiver to join, then act as a sender that ignored the
	// CLI-side cap (a web sender can): encrypt straight to its recipient.
	var conn *fakePipeConn
	for i := 0; i < 500 && conn == nil; i++ {
		room.mu.Lock()
		for _, candidate := range room.conns {
			conn = candidate
		}
		room.mu.Unlock()
		if conn == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if conn == nil {
		t.Fatal("the receiver never joined")
	}
	payload := strings.Repeat("x", 4000)
	sealed, err := sealToRecipient(conn.recipient, []byte(payload))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	room.mu.Lock()
	room.chosenID = conn.id
	room.attached = true
	room.kind = "text"
	room.body = sealed
	room.bumpLocked()
	room.mu.Unlock()

	select {
	case err := <-joined:
		if !errors.Is(err, ErrOverInlineCap) {
			t.Fatalf("err = %v, want ErrOverInlineCap", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the receiver never finished")
	}
	if string(result.Plaintext) != payload {
		t.Errorf("plaintext = %d bytes, want the payload", len(result.Plaintext))
	}
	if result.WrittenPath == "" {
		t.Fatal("the over-limit payload was not saved to the reserved file")
	}
	if got, _ := os.ReadFile(result.WrittenPath); string(got) != payload {
		t.Errorf("saved file = %d bytes, want the payload", len(got))
	}
}

func testRecipient(t *testing.T) string {
	t.Helper()
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return clientcrypto.RecipientStringOf(identity)
}

func openPipeRoom(t *testing.T, fake *fakeFlick) *fakePipeRoom {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var room *fakePipeRoom
	for _, candidate := range fake.pipeRooms {
		room = candidate
	}
	if room == nil {
		t.Fatal("no room was created")
	}
	return room
}

// Both long polls answer when their hold expires without a room change; the
// answers are built under the room lock.
func TestPipeLongPollsAnswerWhenTheHoldExpires(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	room := fake.newPipeRoom()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	joined, err := client.PipeJoin(ctx, room.code, testRecipient(t))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	room.mu.Lock()
	version := room.version
	room.mu.Unlock()

	wait, err := client.PipeWait(ctx, room.code, room.senderToken, version)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if wait.Version != version || len(wait.Connections) != 1 {
		t.Errorf("wait = %+v, want the unchanged room view with one connection", wait)
	}

	status, err := client.PipeStatus(ctx, room.code, joined.ConnectionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status != "waiting" {
		t.Errorf("status = %q, want waiting", status)
	}
}

func TestLiveSendRefusesAnOversizedFileBeforeCreatingARoom(t *testing.T) {
	fake := newFakeFlick(t)
	fake.maxFile = 16
	client := fake.client()
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = LiveSend(ctx, client, LiveSendOptions{
		FileName: "big.bin",
		File:     file,
		Started:  func(PipeSession) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the size refusal", err)
	}

	fake.mu.Lock()
	rooms := len(fake.pipeRooms)
	fake.mu.Unlock()
	if rooms != 0 {
		t.Errorf("a room was created for an undeliverable file: %d rooms", rooms)
	}
}

func TestLiveJoinChecksTheOutputDirectoryBeforeTheStream(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	room := fake.newPipeRoom()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Select the receiver the moment it joins, as a sender with -confirm auto does.
	go func() {
		for {
			room.mu.Lock()
			for _, conn := range room.conns {
				if room.chosenID == "" {
					room.chosenID = conn.id
					room.bumpLocked()
				}
			}
			room.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	_, err := LiveJoin(ctx, client, LiveJoinOptions{
		Code:      room.code,
		OutputDir: filepath.Join(t.TempDir(), "missing"),
		Connected: func(string, int) error { return nil },
	})
	if err == nil {
		t.Fatal("a missing output directory did not stop the join")
	}

	room.mu.Lock()
	defer room.mu.Unlock()
	if room.receiveCalls != 0 {
		t.Errorf("the receive stream was opened %d time(s) before the destination was checked", room.receiveCalls)
	}
}

func TestLiveSendCancelsTheRoomWhenTheFileCannotBeSealed(t *testing.T) {
	fake := newFakeFlick(t)
	fake.maxFile = 16
	client := fake.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	receiverDone := make(chan error, 1)
	go func() {
		var code string
		for i := 0; i < 500 && code == ""; i++ {
			fake.mu.Lock()
			for _, candidate := range fake.pipeRooms {
				code = candidate.code
			}
			fake.mu.Unlock()
			if code == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
		_, err := LiveJoin(ctx, client, LiveJoinOptions{
			Code:      code,
			OutputDir: t.TempDir(),
			Connected: func(string, int) error { return nil },
		})
		receiverDone <- err
	}()

	// A reader with no size passes the up-front check, so the limit bites after select.
	_, err := LiveSend(ctx, client, LiveSendOptions{
		FileName: "stream.bin",
		File:     io.LimitReader(strings.NewReader(strings.Repeat("x", 4096)), 4096),
		Started:  func(PipeSession) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the size refusal after select", err)
	}

	room := openPipeRoom(t, fake)
	room.mu.Lock()
	cancelled := room.cancelCalled
	room.mu.Unlock()
	if !cancelled {
		t.Error("the room was left open after the sender's seal failure")
	}
	select {
	case <-receiverDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the chosen receiver was left waiting after the sender's seal failure")
	}
}

func waitForPipeCode(fake *fakeFlick) string {
	var code string
	for i := 0; i < 500 && code == ""; i++ {
		fake.mu.Lock()
		for _, room := range fake.pipeRooms {
			code = room.code
		}
		fake.mu.Unlock()
		if code == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return code
}

// runLivePair runs a sender and a receiver against each other through the fake
// relay and returns both results.
func runLivePair(t *testing.T, fake *fakeFlick, ctx context.Context, send LiveSendOptions, joinOpts func(code string) LiveJoinOptions) (LiveSendResult, OpenResult) {
	t.Helper()
	client := fake.client()
	type received struct {
		result OpenResult
		err    error
	}
	done := make(chan received, 1)
	go func() {
		result, err := LiveJoin(ctx, client, joinOpts(waitForPipeCode(fake)))
		done <- received{result, err}
	}()

	sent, err := LiveSend(ctx, client, send)
	if err != nil {
		t.Fatalf("live send: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("live join: %v", got.err)
		}
		return sent, got.result
	case <-time.After(30 * time.Second):
		t.Fatal("the receiver never finished")
	}
	return LiveSendResult{}, OpenResult{}
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestLiveSendRefusesADirectoryBeforeCreatingARoom(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	dir := t.TempDir()
	opened, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = LiveSend(ctx, client, LiveSendOptions{
		FileName: "dir",
		File:     opened,
		Started:  func(PipeSession) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("err = %v, want the directory refusal", err)
	}

	fake.mu.Lock()
	rooms := len(fake.pipeRooms)
	fake.mu.Unlock()
	if rooms != 0 {
		t.Errorf("a room was created for a directory: %d rooms", rooms)
	}
}

// The text destination is reserved before the relay can hand out the stream,
// so a join that ends without a stream must take that file back with it.
func TestLiveJoinLeavesNoReservedFileBehindWhenNothingArrives(t *testing.T) {
	for name, ends := range map[string]func(room *fakePipeRoom){
		"the status poll ends first": func(room *fakePipeRoom) {
			go func() {
				for {
					room.mu.Lock()
					joined := len(room.conns) > 0
					if joined {
						room.state = "cancelled"
						room.bumpLocked()
					}
					room.mu.Unlock()
					if joined {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
		},
		"the receive stream fails": func(room *fakePipeRoom) {
			room.mu.Lock()
			room.receiveFails = true
			room.mu.Unlock()
		},
	} {
		for _, withOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/output=%v", name, withOutput), func(t *testing.T) {
				fake := newFakeFlick(t)
				client := fake.client()
				room := fake.newPipeRoom()
				ends(room)
				dir := t.TempDir()
				opts := LiveJoinOptions{Code: room.code, OutputDir: dir, Connected: func(string, int) error { return nil }}
				if withOutput {
					opts.OutputPath = filepath.Join(dir, "reply.txt")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()

				// A sender selects the receiver the moment it joins, so the join gets as
				// far as the stream.
				go func() {
					for {
						room.mu.Lock()
						for _, conn := range room.conns {
							room.chosenID = conn.id
						}
						wasOpen := room.state == "open"
						room.bumpLocked()
						room.mu.Unlock()
						if !wasOpen {
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
				}()

				if _, err := LiveJoin(ctx, client, opts); err == nil {
					t.Fatal("a join with no stream reported success")
				}
				if names := dirEntryNames(t, dir); len(names) != 0 {
					t.Errorf("files left behind: %v", names)
				}
			})
		}
	}
}

func TestLiveJoinSavesTextUnderItsDottedName(t *testing.T) {
	fake := newFakeFlick(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()

	_, received := runLivePair(t, fake, ctx,
		LiveSendOptions{Text: "live payload", Started: func(PipeSession) error { return nil }},
		func(code string) LiveJoinOptions {
			return LiveJoinOptions{Code: code, OutputDir: dir, Connected: func(string, int) error { return nil }}
		})

	code := openPipeRoom(t, fake).code
	if base := filepath.Base(received.WrittenPath); base != "flick-live-"+code+".txt" {
		t.Errorf("written = %q, want flick-live-%s.txt", base, code)
	}
	if names := dirEntryNames(t, dir); len(names) != 1 {
		t.Errorf("output directory holds %v, want only the saved text", names)
	}
	if got, _ := os.ReadFile(received.WrittenPath); string(got) != "live payload" {
		t.Errorf("saved %q", got)
	}
}

func TestLiveJoinWritesToTheExactOutputPath(t *testing.T) {
	for name, send := range map[string]LiveSendOptions{
		"text": {Text: "live payload", Started: func(PipeSession) error { return nil }},
		"file": {FileName: "notes.txt", File: strings.NewReader("file over live"), Started: func(PipeSession) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeFlick(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			target := filepath.Join(dir, "reply.bin")

			_, received := runLivePair(t, fake, ctx, send, func(code string) LiveJoinOptions {
				return LiveJoinOptions{Code: code, OutputPath: target, Connected: func(string, int) error { return nil }}
			})

			if received.WrittenPath != target {
				t.Fatalf("written = %q, want %s", received.WrittenPath, target)
			}
			if names := dirEntryNames(t, dir); len(names) != 1 {
				t.Errorf("output directory holds %v, want only the requested file", names)
			}
			want := "live payload"
			if send.FileName != "" {
				want = "file over live"
			}
			if got, _ := os.ReadFile(target); string(got) != want {
				t.Errorf("saved %q, want %q", got, want)
			}
		})
	}
}

func pipeRoomByCode(fake *fakeFlick, code string) *fakePipeRoom {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.pipeRooms[code]
}

// A sender whose transfer fails after select releases the room, so the chosen
// receiver is not left attached and waiting.
func TestLiveSendReleasesTheRoomWhenTheSendFails(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	senderDone := make(chan error, 1)
	go func() {
		_, err := LiveSend(ctx, client, LiveSendOptions{
			Text:    "never arrives",
			Started: func(PipeSession) error { return nil },
		})
		senderDone <- err
	}()

	// The sender's room then gets a relay that refuses the stream and a receiver.
	code := waitForPipeCode(fake)
	if code == "" {
		t.Fatal("no room appeared")
	}
	room := pipeRoomByCode(fake, code)
	room.mu.Lock()
	room.sendFails = true
	room.mu.Unlock()
	receiverDone := make(chan error, 1)
	go func() {
		_, err := LiveJoin(ctx, client, LiveJoinOptions{
			Code:      code,
			OutputDir: t.TempDir(),
			Connected: func(string, int) error { return nil },
		})
		receiverDone <- err
	}()

	select {
	case err := <-senderDone:
		if err == nil {
			t.Fatal("a failed send reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the sender never finished")
	}
	room.mu.Lock()
	cancelled := room.cancelCalled
	room.mu.Unlock()
	if !cancelled {
		t.Error("the room was left open after the failed send")
	}
	select {
	case <-receiverDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the chosen receiver was left waiting after the failed send")
	}
}

// Ctrl-C that lands with the pick answer fails the select; the room is still released.
func TestLiveSendReleasesTheRoomWhenTheContextEndsAtSelect(t *testing.T) {
	fake := newFakeFlick(t)
	client := fake.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	senderDone := make(chan error, 1)
	go func() {
		_, err := LiveSend(ctx, client, LiveSendOptions{
			Text: "never arrives",
			// The pick is consulted only once a device is waiting.
			Pick: func([]PipePending) (LiveDecision, error) {
				cancel()
				return LiveDecision{Action: "send"}, nil
			},
			Started: func(PipeSession) error { return nil },
		})
		senderDone <- err
	}()

	code := waitForPipeCode(fake)
	if code == "" {
		t.Fatal("no room appeared")
	}
	receiverDone := make(chan error, 1)
	go func() {
		_, err := LiveJoin(ctx, client, LiveJoinOptions{
			Code:      code,
			OutputDir: t.TempDir(),
			Connected: func(string, int) error { return nil },
		})
		receiverDone <- err
	}()

	select {
	case err := <-senderDone:
		if err == nil {
			t.Fatal("a cancelled send reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the sender never finished")
	}
	room := pipeRoomByCode(fake, code)
	deadline := time.Now().Add(10 * time.Second)
	for {
		room.mu.Lock()
		cancelled := room.cancelCalled
		room.mu.Unlock()
		if cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the room was left open after Ctrl-C at the pick")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-receiverDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the receiver was left waiting after Ctrl-C at the pick")
	}
}

// One 5xx from each long poll, as from a replica stopping in a rolling update,
// must not end a transfer.
func TestLiveRoundTripSurvivesOneFailedLongPoll(t *testing.T) {
	fake := newFakeFlick(t)
	fake.statusFailures = 1
	fake.waitFailures = 1
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, received := runLivePair(t, fake, ctx,
		LiveSendOptions{Text: "live payload", Started: func(PipeSession) error { return nil }},
		func(code string) LiveJoinOptions {
			return LiveJoinOptions{Code: code, OutputDir: t.TempDir(), Connected: func(string, int) error { return nil }}
		})

	if string(received.Plaintext) != "live payload" {
		t.Errorf("received %q", received.Plaintext)
	}
}

func TestPipeLongPollRetriesOneServerErrorOnly(t *testing.T) {
	for name, x := range map[string]struct {
		statuses  []int
		wantErr   bool
		wantCalls int
	}{
		"one 503 then 200":          {statuses: []int{503, 200}, wantCalls: 2},
		"two 503s":                  {statuses: []int{503, 503}, wantErr: true, wantCalls: 2},
		"a 404 is the relay answer": {statuses: []int{404}, wantErr: true, wantCalls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				status := x.statuses[min(calls-1, len(x.statuses)-1)]
				if status == http.StatusOK {
					writeJSON(w, status, map[string]string{"status": "waiting"})
					return
				}
				writeError(w, status, "error")
			}))
			defer server.Close()

			_, err := NewClient(server.URL, server.Client()).PipeStatus(context.Background(), "305817", "connection")
			if (err != nil) != x.wantErr {
				t.Errorf("err = %v, wantErr %v", err, x.wantErr)
			}
			if calls != x.wantCalls {
				t.Errorf("the relay answered %d calls, want %d", calls, x.wantCalls)
			}
		})
	}
}
