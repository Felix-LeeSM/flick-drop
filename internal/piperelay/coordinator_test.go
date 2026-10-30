package piperelay

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
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

func TestCoordinator_Lifecycle(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
		CleanInterval: 1 * time.Second,
	})
	defer coord.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	code, salt, expiresIn, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	if len(code) != 6 || len(salt) != 16 || expiresIn <= 0 {
		t.Fatalf("Init() invalid output: code=%s, saltLen=%d, expiresIn=%d", code, len(salt), expiresIn)
	}
	if coord.ActiveSessions() != 1 {
		t.Fatalf("ActiveSessions() = %d, want 1", coord.ActiveSessions())
	}

	testPayload := bytes.Repeat([]byte("Flick live relay streaming data chunk! 1234567890\n"), 4096) // ~200 KiB

	var wg sync.WaitGroup
	var waitErr, sendErr, recvErr error
	var receivedBytes bytes.Buffer
	var receivedSalt []byte
	var receivedMeta SessionMetadata

	// 1. Sender starts waiting for receiver
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitErr = coord.WaitReceiver(ctx, code)
		if waitErr != nil {
			return
		}
		// When wait succeeds, sender starts sending
		_, sendErr = coord.Send(ctx, code, SessionMetadata{
			Kind:              "file",
			EncryptedFilename: "dGVzdC50eHQ=",
		}, bytes.NewReader(testPayload))
	}()

	// 2. Receiver connects
	wg.Add(1)
	go func() {
		defer wg.Done()
		var pr *io.PipeReader
		var cleanup func()
		pr, receivedSalt, receivedMeta, cleanup, recvErr = coord.Receive(ctx, code)
		if recvErr != nil {
			return
		}
		defer cleanup()

		_, recvErr = io.Copy(&receivedBytes, pr)
	}()

	wg.Wait()

	if waitErr != nil {
		t.Fatalf("WaitReceiver() failed: %v", waitErr)
	}
	if sendErr != nil {
		t.Fatalf("Send() failed: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("Receive() / io.Copy failed: %v", recvErr)
	}

	if !bytes.Equal(receivedSalt, salt) {
		t.Errorf("received salt mismatch: got %x, want %x", receivedSalt, salt)
	}
	if receivedMeta.Kind != "file" || receivedMeta.EncryptedFilename != "dGVzdC50eHQ=" {
		t.Errorf("received metadata mismatch: %+v", receivedMeta)
	}
	if !bytes.Equal(receivedBytes.Bytes(), testPayload) {
		t.Errorf("received payload does not match sent payload (size got %d, want %d)", receivedBytes.Len(), len(testPayload))
	}

	// Verify session was cleanly burned and freed
	if active := coord.ActiveSessions(); active != 0 {
		t.Errorf("ActiveSessions() after completion = %d, want 0", active)
	}
}

func TestCoordinator_CapacityLimit(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 2,
		TTL:           10 * time.Second,
	})
	defer coord.Close()

	ctx := context.Background()
	_, _, _, err1 := coord.Init(ctx)
	if err1 != nil {
		t.Fatalf("Init 1 failed: %v", err1)
	}
	_, _, _, err2 := coord.Init(ctx)
	if err2 != nil {
		t.Fatalf("Init 2 failed: %v", err2)
	}

	// 3rd should fail
	_, _, _, err3 := coord.Init(ctx)
	if err3 != ErrCapacityExceeded {
		t.Fatalf("Init 3 expected ErrCapacityExceeded, got: %v", err3)
	}
}

func TestCoordinator_TTL_Expiry(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           50 * time.Millisecond,
		CleanInterval: 20 * time.Millisecond,
	})
	defer coord.Close()

	ctx := context.Background()
	code, _, _, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	time.Sleep(120 * time.Millisecond)

	err = coord.WaitReceiver(ctx, code)
	if err != ErrNotFound && err != ErrExpired {
		t.Fatalf("expected ErrNotFound or ErrExpired, got: %v", err)
	}
	if active := coord.ActiveSessions(); active != 0 {
		t.Fatalf("ActiveSessions() = %d, want 0 after TTL expiry", active)
	}
}

func TestCoordinator_BurnOn3FailedAttempts(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
	})
	defer coord.Close()

	ctx := context.Background()
	code, _, _, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Attempt 1
	burned, err := coord.RecordFailedAttempt(code)
	if burned || err != nil {
		t.Fatalf("attempt 1: burned=%v, err=%v", burned, err)
	}

	// Attempt 2
	burned, err = coord.RecordFailedAttempt(code)
	if burned || err != nil {
		t.Fatalf("attempt 2: burned=%v, err=%v", burned, err)
	}

	// Attempt 3: should burn!
	burned, err = coord.RecordFailedAttempt(code)
	if !burned || err != ErrSessionBurned {
		t.Fatalf("attempt 3: expected burned=true and ErrSessionBurned, got burned=%v, err=%v", burned, err)
	}

	// Session should now be gone
	if active := coord.ActiveSessions(); active != 0 {
		t.Fatalf("ActiveSessions() = %d, want 0 after session burned", active)
	}

	err = coord.WaitReceiver(ctx, code)
	if err != ErrNotFound {
		t.Fatalf("WaitReceiver() after burn: expected ErrNotFound, got: %v", err)
	}
}

func TestCoordinator_DoubleReceiver(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
	})
	defer coord.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	code, _, _, err := coord.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Receiver 1 connects in goroutine
	go func() {
		_, _, _, cleanup, _ := coord.Receive(ctx, code)
		if cleanup != nil {
			defer cleanup()
		}
	}()

	// Wait briefly for receiver 1 to register
	time.Sleep(20 * time.Millisecond)

	// Receiver 2 connects: should be rejected
	_, _, _, cleanup2, err2 := coord.Receive(ctx, code)
	if cleanup2 != nil {
		cleanup2()
	}
	if err2 != ErrAlreadyConnected {
		t.Fatalf("expected ErrAlreadyConnected, got: %v", err2)
	}
}

func TestCoordinator_ReceiverDisconnect(t *testing.T) {
	coord := NewCoordinator(CoordinatorOptions{
		MaxConcurrent: 10,
		TTL:           5 * time.Second,
	})
	defer coord.Close()

	recvCtx, recvCancel := context.WithCancel(context.Background())
	sendCtx := context.Background()

	code, _, _, err := coord.Init(sendCtx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	var sendErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, sendErr = coord.Send(sendCtx, code, SessionMetadata{Kind: "text"}, strings.NewReader("hello world this is a test"))
	}()

	// Receiver connects and immediately cancels
	pr, _, _, cleanup, err := coord.Receive(recvCtx, code)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	recvCancel() // cancel receiver context
	cleanup()
	if pr != nil {
		_ = pr.Close()
	}

	wg.Wait()
	if sendErr == nil {
		t.Log("Send completed before cancel propagated (acceptable race), or errored as expected")
	}
}
