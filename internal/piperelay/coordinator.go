package piperelay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"
)

var (
	ErrCapacityExceeded = errors.New("piperelay: maximum concurrent pipe sessions reached")
	ErrNotFound         = errors.New("piperelay: rendezvous session not found or expired")
	ErrSessionClosed    = errors.New("piperelay: rendezvous session closed")
	ErrSessionBurned    = errors.New("piperelay: rendezvous session burned due to excessive failed attempts")
	ErrAlreadyConnected = errors.New("piperelay: party already connected to session")
	ErrInvalidCode      = errors.New("piperelay: code must be a 6-digit numeric string")
	ErrExpired          = errors.New("piperelay: rendezvous session expired")
)

const (
	defaultMaxConcurrent       = 100
	defaultTTL                 = 3 * time.Minute
	defaultCleanInterval       = 30 * time.Second
	defaultMaxTransferDuration = 10 * time.Minute
	saltLengthBytes            = 16
	pipeChunkBufferBytes       = 64 * 1024 // 64 KiB buffer matching age streaming chunk size
)

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, pipeChunkBufferBytes)
		return &b
	},
}

// SessionMetadata carries lightweight, non-secret file/text descriptor headers.
type SessionMetadata struct {
	Kind              string // e.g. "text" or "file"
	EncryptedFilename string // base64-encoded encrypted filename (if file)
}

// Coordinator manages in-memory live relay rendezvous sessions.
type Coordinator struct {
	mu                  sync.RWMutex
	sessions            map[[32]byte]*Session
	maxConcurrent       int
	ttl                 time.Duration
	cleanInterval       time.Duration
	maxTransferDuration time.Duration
	stopClean           chan struct{}
	cleanDone           chan struct{}
}

// CoordinatorOptions configures the in-memory coordinator.
type CoordinatorOptions struct {
	MaxConcurrent       int
	TTL                 time.Duration
	CleanInterval       time.Duration
	MaxTransferDuration time.Duration
}

// Session tracks the rendezvous lifecycle between sender and receiver.
type Session struct {
	codeHash  [32]byte
	salt      []byte
	createdAt time.Time
	expiresAt time.Time

	mu             sync.Mutex
	failedAttempts int
	burned         bool

	kind              string
	encryptedFilename string

	receiverReady chan struct{}
	senderReady   chan struct{}
	done          chan struct{}

	pipeReader *io.PipeReader
	pipeWriter *io.PipeWriter

	receiverOnce sync.Once
	senderOnce   sync.Once
	closeOnce    sync.Once
}

// NewCoordinator initializes an in-memory coordinator and starts background GC.
func NewCoordinator(opts CoordinatorOptions) *Coordinator {
	maxConcurrent := opts.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	cleanInterval := opts.CleanInterval
	if cleanInterval <= 0 {
		cleanInterval = defaultCleanInterval
	}
	maxTransferDuration := opts.MaxTransferDuration
	if maxTransferDuration <= 0 {
		maxTransferDuration = defaultMaxTransferDuration
	}

	c := &Coordinator{
		sessions:            make(map[[32]byte]*Session),
		maxConcurrent:       maxConcurrent,
		ttl:                 ttl,
		cleanInterval:       cleanInterval,
		maxTransferDuration: maxTransferDuration,
		stopClean:           make(chan struct{}),
		cleanDone:           make(chan struct{}),
	}

	go c.runCleaner()
	return c
}

// NormalizeCode strips spaces and dashes, validating that the remainder is 6 digits.
func NormalizeCode(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	normalized := strings.ReplaceAll(trimmed, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	if len(normalized) != 6 {
		return "", ErrInvalidCode
	}
	for i := 0; i < len(normalized); i++ {
		if normalized[i] < '0' || normalized[i] > '9' {
			return "", ErrInvalidCode
		}
	}
	return normalized, nil
}

// GenerateCode generates a cryptographically random 6-digit numeric string (100000..999999).
func GenerateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", fmt.Errorf("generate random code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()+100000), nil
}

// Init creates a new rendezvous session, generating an uncolliding 6-digit code and random salt.
func (c *Coordinator) Init(ctx context.Context) (code string, salt []byte, expiresIn int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.sessions) >= c.maxConcurrent {
		return "", nil, 0, ErrCapacityExceeded
	}

	var generatedCode string
	var hash [32]byte
	for attempts := 0; attempts < 10; attempts++ {
		gCode, err := GenerateCode()
		if err != nil {
			return "", nil, 0, err
		}
		candidateHash := sha256.Sum256([]byte(gCode))
		if _, exists := c.sessions[candidateHash]; !exists {
			generatedCode = gCode
			hash = candidateHash
			break
		}
	}
	if generatedCode == "" {
		return "", nil, 0, errors.New("piperelay: failed to generate unique code")
	}

	salt = make([]byte, saltLengthBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", nil, 0, fmt.Errorf("generate salt: %w", err)
	}

	pr, pw := io.Pipe()
	now := time.Now()
	s := &Session{
		codeHash:      hash,
		salt:          salt,
		createdAt:     now,
		expiresAt:     now.Add(c.ttl),
		receiverReady: make(chan struct{}),
		senderReady:   make(chan struct{}),
		done:          make(chan struct{}),
		pipeReader:    pr,
		pipeWriter:    pw,
	}

	c.sessions[hash] = s
	return generatedCode, salt, int(c.ttl.Seconds()), nil
}

// WaitReceiver waits for a receiver to connect to the session.
func (c *Coordinator) WaitReceiver(ctx context.Context, rawCode string) error {
	normalized, err := NormalizeCode(rawCode)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(normalized))

	c.mu.RLock()
	s, ok := c.sessions[hash]
	c.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	select {
	case <-s.receiverReady:
		return nil
	case <-s.done:
		return ErrSessionClosed
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(s.expiresAt)):
		return ErrExpired
	}
}

// Receive connects the receiver to the rendezvous session, waiting for sender metadata.
func (c *Coordinator) Receive(ctx context.Context, rawCode string) (*io.PipeReader, []byte, SessionMetadata, func(), error) {
	normalized, err := NormalizeCode(rawCode)
	if err != nil {
		return nil, nil, SessionMetadata{}, nil, err
	}
	hash := sha256.Sum256([]byte(normalized))

	c.mu.Lock()
	s, ok := c.sessions[hash]
	if !ok {
		c.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, ErrNotFound
	}

	s.mu.Lock()
	if s.burned {
		s.mu.Unlock()
		c.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, ErrSessionBurned
	}
	s.mu.Unlock()

	alreadyConnected := false
	s.receiverOnce.Do(func() {
		close(s.receiverReady)
		alreadyConnected = true
	})
	c.mu.Unlock()

	if !alreadyConnected {
		return nil, nil, SessionMetadata{}, nil, ErrAlreadyConnected
	}

	cleanup := func() {
		s.closeOnce.Do(func() {
			close(s.done)
			_ = s.pipeReader.CloseWithError(io.EOF)
			_ = s.pipeWriter.CloseWithError(io.EOF)
		})
		c.burnSession(hash)
	}

	select {
	case <-s.senderReady:
		s.mu.Lock()
		meta := SessionMetadata{
			Kind:              s.kind,
			EncryptedFilename: s.encryptedFilename,
		}
		salt := make([]byte, len(s.salt))
		copy(salt, s.salt)
		s.mu.Unlock()
		return s.pipeReader, salt, meta, cleanup, nil
	case <-s.done:
		return nil, nil, SessionMetadata{}, nil, ErrSessionClosed
	case <-ctx.Done():
		cleanup()
		return nil, nil, SessionMetadata{}, nil, ctx.Err()
	case <-time.After(time.Until(s.expiresAt)):
		cleanup()
		return nil, nil, SessionMetadata{}, nil, ErrExpired
	}
}

// Send sets stream metadata, waits for receiver readiness, and copies body to pipe.
func (c *Coordinator) Send(ctx context.Context, rawCode string, meta SessionMetadata, body io.Reader) (int64, error) {
	normalized, err := NormalizeCode(rawCode)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256([]byte(normalized))

	c.mu.Lock()
	s, ok := c.sessions[hash]
	if !ok {
		c.mu.Unlock()
		return 0, ErrNotFound
	}

	s.mu.Lock()
	if s.burned {
		s.mu.Unlock()
		c.mu.Unlock()
		return 0, ErrSessionBurned
	}
	s.kind = meta.Kind
	s.encryptedFilename = meta.EncryptedFilename
	s.mu.Unlock()

	alreadySending := false
	s.senderOnce.Do(func() {
		close(s.senderReady)
		alreadySending = true
	})
	c.mu.Unlock()

	if !alreadySending {
		return 0, ErrAlreadyConnected
	}

	select {
	case <-s.receiverReady:
	case <-s.done:
		return 0, ErrSessionClosed
	case <-ctx.Done():
		_ = s.pipeWriter.CloseWithError(ctx.Err())
		c.burnSession(hash)
		return 0, ctx.Err()
	case <-time.After(time.Until(s.expiresAt)):
		_ = s.pipeWriter.CloseWithError(ErrExpired)
		c.burnSession(hash)
		return 0, ErrExpired
	}

	ptr := bufferPool.Get().(*[]byte)
	buf := *ptr
	defer func() {
		clear(buf)
		bufferPool.Put(ptr)
	}()

	n, copyErr := io.CopyBuffer(s.pipeWriter, body, buf)
	if copyErr != nil {
		_ = s.pipeWriter.CloseWithError(copyErr)
		c.burnSession(hash)
		return n, copyErr
	}

	_ = s.pipeWriter.Close() // signals clean EOF to PipeReader
	return n, nil
}

// RecordFailedAttempt registers a failed code attempt, burning the session if attempts >= 3.
func (c *Coordinator) RecordFailedAttempt(rawCode string) (burned bool, err error) {
	normalized, err := NormalizeCode(rawCode)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256([]byte(normalized))

	c.mu.Lock()
	defer c.mu.Unlock()

	s, ok := c.sessions[hash]
	if !ok {
		return false, ErrNotFound
	}

	s.mu.Lock()
	s.failedAttempts++
	if s.failedAttempts >= 3 {
		s.burned = true
		s.mu.Unlock()
		c.removeLocked(hash, ErrSessionBurned)
		return true, ErrSessionBurned
	}
	s.mu.Unlock()
	return false, nil
}

// ActiveSessions returns the current number of in-flight sessions (for telemetry/tests).
func (c *Coordinator) ActiveSessions() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sessions)
}

func (c *Coordinator) burnSession(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(hash, ErrSessionClosed)
}

func (c *Coordinator) removeLocked(hash [32]byte, cause error) {
	s, ok := c.sessions[hash]
	if !ok {
		return
	}
	delete(c.sessions, hash)

	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.pipeWriter.CloseWithError(cause)
		_ = s.pipeReader.CloseWithError(cause)
	})
}

func (c *Coordinator) runCleaner() {
	ticker := time.NewTicker(c.cleanInterval)
	defer ticker.Stop()
	defer close(c.cleanDone)

	for {
		select {
		case <-c.stopClean:
			return
		case now := <-ticker.C:
			c.cleanExpired(now)
		}
	}
}

func (c *Coordinator) cleanExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for hash, s := range c.sessions {
		if now.After(s.expiresAt) {
			c.removeLocked(hash, ErrExpired)
		}
	}
}

// Close gracefully terminates the cleaner and closes all active sessions.
func (c *Coordinator) Close() error {
	c.mu.Lock()
	select {
	case <-c.stopClean:
		c.mu.Unlock()
		return nil
	default:
		close(c.stopClean)
	}

	for hash := range c.sessions {
		c.removeLocked(hash, ErrSessionClosed)
	}
	c.mu.Unlock()

	<-c.cleanDone
	return nil
}
