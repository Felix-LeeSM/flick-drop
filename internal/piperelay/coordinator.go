package piperelay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrCapacityExceeded     = errors.New("piperelay: maximum concurrent pipe sessions reached")
	ErrNotFound             = errors.New("piperelay: rendezvous session or connection not found or expired")
	ErrSessionClosed        = errors.New("piperelay: rendezvous session closed")
	ErrCancelled            = errors.New("piperelay: sender cancelled the rendezvous session")
	ErrAlreadyConnected     = errors.New("piperelay: another receiver already holds the transfer")
	ErrRoomFull             = errors.New("piperelay: too many receivers are waiting on this session")
	ErrDeclined             = errors.New("piperelay: sender declined this connection")
	ErrNotSelected          = errors.New("piperelay: sender chose another connection")
	ErrNotPending           = errors.New("piperelay: connection is no longer waiting for a decision")
	ErrConfirmationDisabled = errors.New("piperelay: session does not require confirmation")
	ErrStreaming            = errors.New("piperelay: transfer is already streaming")
	ErrNetworkFull          = errors.New("piperelay: too many receivers from one client network are waiting on this session")
	ErrAwaitingDecision     = errors.New("piperelay: connection is still waiting for the sender's pick")
	ErrInvalidCode          = errors.New("piperelay: code must be a 6-digit numeric string")
	ErrExpired              = errors.New("piperelay: rendezvous session expired")
)

const (
	defaultMaxConcurrent       = 100
	defaultTTL                 = 3 * time.Minute
	defaultCleanInterval       = 30 * time.Second
	defaultMaxTransferDuration = 10 * time.Minute
	// defaultPresenceTimeout is how long a joined connection stays in the room
	// without an open Status poll, and how long a chosen connection may take to
	// open Receive. Status holds at most 25 seconds in the HTTP layer
	// (internal/httpapi/piperelay.go pipeWaitMaxHold), so a receiver that keeps
	// polling is never idle this long.
	defaultPresenceTimeout  = 35 * time.Second
	saltLengthBytes         = 16
	tokenLengthBytes        = 32
	connectionIDLengthBytes = 16
	pipeChunkBufferBytes    = 64 * 1024 // 64 KiB buffer matching age streaming chunk size
	// MaxPendingConnections caps the receivers one session holds undecided at
	// once. The next joiner gets ErrRoomFull; the session itself stays alive.
	MaxPendingConnections = 10
	// MaxPendingPerNetwork caps the undecided receivers from one client network
	// (an IPv4 address or an IPv6 /64, derived by the HTTP layer) in one
	// session, so one network cannot fill the room alone.
	MaxPendingPerNetwork = 3
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

// InitOptions carries the sender's choices for a new session.
type InitOptions struct {
	// RequireConfirmation turns the session into a waiting room: receivers wait
	// until the sender picks one. Off keeps first-connector-wins.
	RequireConfirmation bool
}

// InitResult is what the sender gets back from Init. SenderToken is the only
// credential for the sender-side calls (Wait, Select, Decline, Cancel, Send).
type InitResult struct {
	Code                string
	SenderToken         string
	Salt                []byte
	ExpiresIn           int
	RequireConfirmation bool
}

// JoinResult is what a receiver gets back from Join. VerifyCode and Device are
// empty when the session does not require confirmation.
type JoinResult struct {
	ConnectionID        string
	VerifyCode          string
	Device              string
	RequireConfirmation bool
	// ExpiresIn is the seconds left in the rendezvous window.
	ExpiresIn int
}

// PendingConnection is one undecided receiver as the sender sees it.
type PendingConnection struct {
	ID         string
	Index      int
	VerifyCode string
	Device     string
	JoinedAt   time.Time
}

// WaitSnapshot is the sender's view of a session at Version.
type WaitSnapshot struct {
	Version uint64
	// ReceiverReady is true once the chosen receiver is attached and Send can stream.
	ReceiverReady bool
	// Pending lists undecided receivers in join order; empty when confirmation is off.
	Pending []PendingConnection
}

// Coordinator manages in-memory live relay rendezvous sessions.
type Coordinator struct {
	mu                  sync.RWMutex
	sessions            map[[32]byte]*Session
	maxConcurrent       int
	ttl                 time.Duration
	cleanInterval       time.Duration
	maxTransferDuration time.Duration
	presenceTimeout     time.Duration
	stopClean           chan struct{}
	cleanDone           chan struct{}
}

// CoordinatorOptions configures the in-memory coordinator.
type CoordinatorOptions struct {
	MaxConcurrent       int
	TTL                 time.Duration
	CleanInterval       time.Duration
	MaxTransferDuration time.Duration
	// PresenceTimeout defaults to defaultPresenceTimeout (35 seconds).
	PresenceTimeout time.Duration
}

// connection is one receiver that entered the right code. decided closes once
// the sender (or first-connector-wins) settles it; result is nil for the chosen
// connection and the reason otherwise.
//
// A waiting connection counts as present only while it polls Status: polls is
// the number of open Status calls and lastSeen the last time one started or
// ended. Session.pruneLocked drops a waiting connection idle longer than the
// presence timeout.
type connection struct {
	id         string
	index      int
	verifyCode string
	device     string
	clientKey  ClientKey
	network    ClientNetwork
	joinedAt   time.Time
	lastSeen   time.Time
	polls      int
	attached   bool
	decided    chan struct{}
	result     error
}

func (conn *connection) isDecided() bool {
	select {
	case <-conn.decided:
		return true
	default:
		return false
	}
}

// ClientKey identifies a receiving client for the per-session decline block.
// The HTTP layer derives it as a hash of the client IP and User-Agent; this
// package never sees either value.
type ClientKey [32]byte

// ClientNetwork identifies a receiving client's network (IPv4 address or IPv6
// /64) for the MaxPendingPerNetwork cap. The HTTP layer derives it as a hash;
// this package never sees the address.
type ClientNetwork [32]byte

// JoinClient is what Join knows about a receiving client.
type JoinClient struct {
	// Device is the coarse label shown to the sender, such as "Safari on iPhone".
	Device  string
	Key     ClientKey
	Network ClientNetwork
}

// Session tracks the rendezvous lifecycle between sender and receivers.
type Session struct {
	codeHash            [32]byte
	senderTokenHash     [32]byte
	salt                []byte
	requireConfirmation bool
	createdAt           time.Time
	expiresAt           time.Time
	presenceTimeout     time.Duration

	mu          sync.Mutex
	connections map[string]*connection
	nextIndex   int
	// declinedClients holds the client keys the sender declined. It lives only
	// in memory for the session's lifetime and is never logged.
	declinedClients map[ClientKey]struct{}
	selected        *connection
	selectedAt      time.Time
	streaming       bool
	streamStartedAt time.Time
	ended           bool
	endCause        error
	version         uint64
	changed         chan struct{}

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
	presenceTimeout := opts.PresenceTimeout
	if presenceTimeout <= 0 {
		presenceTimeout = defaultPresenceTimeout
	}

	c := &Coordinator{
		sessions:            make(map[[32]byte]*Session),
		maxConcurrent:       maxConcurrent,
		ttl:                 ttl,
		cleanInterval:       cleanInterval,
		maxTransferDuration: maxTransferDuration,
		presenceTimeout:     presenceTimeout,
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

// generateVerifyCode draws a 3-digit verification number (000..999) from
// crypto/rand. It is independent of the 6-digit code by construction.
func generateVerifyCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000))
	if err != nil {
		return "", fmt.Errorf("generate verify code: %w", err)
	}
	return fmt.Sprintf("%03d", n.Int64()), nil
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Init creates a new rendezvous session, generating an uncolliding 6-digit
// code, a random salt, and the sender token.
func (c *Coordinator) Init(ctx context.Context, opts InitOptions) (InitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.sessions) >= c.maxConcurrent {
		return InitResult{}, ErrCapacityExceeded
	}

	var generatedCode string
	var hash [32]byte
	for attempts := 0; attempts < 10; attempts++ {
		gCode, err := GenerateCode()
		if err != nil {
			return InitResult{}, err
		}
		candidateHash := sha256.Sum256([]byte(gCode))
		if _, exists := c.sessions[candidateHash]; !exists {
			generatedCode = gCode
			hash = candidateHash
			break
		}
	}
	if generatedCode == "" {
		return InitResult{}, errors.New("piperelay: failed to generate unique code")
	}

	salt := make([]byte, saltLengthBytes)
	if _, err := rand.Read(salt); err != nil {
		return InitResult{}, fmt.Errorf("generate salt: %w", err)
	}
	senderToken, err := randomToken(tokenLengthBytes)
	if err != nil {
		return InitResult{}, fmt.Errorf("generate sender token: %w", err)
	}

	pr, pw := io.Pipe()
	now := time.Now().UTC()
	s := &Session{
		codeHash:            hash,
		senderTokenHash:     sha256.Sum256([]byte(senderToken)),
		salt:                salt,
		requireConfirmation: opts.RequireConfirmation,
		createdAt:           now,
		expiresAt:           now.Add(c.ttl),
		presenceTimeout:     c.presenceTimeout,
		connections:         make(map[string]*connection),
		declinedClients:     make(map[ClientKey]struct{}),
		version:             1,
		changed:             make(chan struct{}),
		receiverReady:       make(chan struct{}),
		senderReady:         make(chan struct{}),
		done:                make(chan struct{}),
		pipeReader:          pr,
		pipeWriter:          pw,
	}

	c.sessions[hash] = s
	return InitResult{
		Code:                generatedCode,
		SenderToken:         senderToken,
		Salt:                salt,
		ExpiresIn:           int(c.ttl.Seconds()),
		RequireConfirmation: opts.RequireConfirmation,
	}, nil
}

// lookup finds a live session by its 6-digit code.
func (c *Coordinator) lookup(rawCode string) (*Session, [32]byte, error) {
	normalized, err := NormalizeCode(rawCode)
	if err != nil {
		return nil, [32]byte{}, err
	}
	hash := sha256.Sum256([]byte(normalized))
	c.mu.RLock()
	s, ok := c.sessions[hash]
	c.mu.RUnlock()
	if !ok {
		return nil, hash, ErrNotFound
	}
	return s, hash, nil
}

// lookupSender finds a session and checks the sender token. A wrong token
// answers exactly like an unknown code, so sender endpoints are no code oracle.
func (c *Coordinator) lookupSender(rawCode, senderToken string) (*Session, [32]byte, error) {
	s, hash, err := c.lookup(rawCode)
	if err != nil {
		return nil, hash, err
	}
	given := sha256.Sum256([]byte(senderToken))
	if senderToken == "" || subtle.ConstantTimeCompare(given[:], s.senderTokenHash[:]) != 1 {
		return nil, hash, ErrNotFound
	}
	return s, hash, nil
}

// notifyLocked wakes every Wait long-poll. Caller holds s.mu.
func (s *Session) notifyLocked() {
	s.version++
	close(s.changed)
	s.changed = make(chan struct{})
}

// expiredLocked reports whether the rendezvous window closed. A streaming
// session is exempt; Coordinator.cleanExpired bounds it by maxTransferDuration.
// Caller holds s.mu.
func (s *Session) expiredLocked(now time.Time) bool {
	return !s.streaming && now.After(s.expiresAt)
}

// pruneLocked drops every waiting connection that has had no open Status poll
// for the presence timeout, so a joiner that never polls (or stopped) frees its
// slot. Dropping is not a decision: nothing is blocked and nothing burns.
// It wakes Wait when it dropped anything. Caller holds s.mu.
func (s *Session) pruneLocked(now time.Time) {
	dropped := false
	for id, conn := range s.connections {
		idle := !conn.isDecided() && !conn.attached && conn.polls == 0 && now.Sub(conn.lastSeen) > s.presenceTimeout
		if idle {
			delete(s.connections, id)
			dropped = true
		}
	}
	if dropped {
		s.notifyLocked()
	}
}

// chosenAbandonedLocked reports whether the chosen connection never opened
// Receive within the presence timeout after Select. Caller holds s.mu.
func (s *Session) chosenAbandonedLocked(now time.Time) bool {
	return s.selected != nil && !s.selected.attached && now.Sub(s.selectedAt) > s.presenceTimeout
}

func (s *Session) pendingLocked() []*connection {
	pending := make([]*connection, 0, len(s.connections))
	for _, conn := range s.connections {
		if !conn.isDecided() {
			pending = append(pending, conn)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].index < pending[j].index })
	return pending
}

// selectLocked hands the transfer to conn and settles every other undecided
// connection with loserCause. Caller holds s.mu.
func (s *Session) selectLocked(conn *connection, loserCause error) {
	s.selected = conn
	s.selectedAt = time.Now()
	close(conn.decided)
	for _, other := range s.connections {
		if other != conn && !other.isDecided() {
			other.result = loserCause
			close(other.decided)
		}
	}
	s.notifyLocked()
}

func (s *Session) endedErrLocked() error {
	if s.endCause != nil {
		return s.endCause
	}
	return ErrSessionClosed
}

// endLocked marks the session ended with cause (the first cause wins) and
// returns the cause that stuck. The caller then removes the session with
// dropLocked. Marking happens under s.mu, the same lock Send holds to set
// streaming, so a session either ends before its stream starts or streams
// before anything can end it. Caller holds s.mu.
func (s *Session) endLocked(cause error) error {
	s.ended = true
	if s.endCause == nil {
		s.endCause = cause
	}
	return s.endCause
}

// Join admits a receiver that entered the right code and returns its
// connection. With confirmation on, each connection gets its own random
// verification number, unique among the session's undecided connections.
// A late joiner (transfer already claimed), an 11th waiting joiner, a 4th
// waiting joiner from one client network, a client the sender already
// declined, or a join after the rendezvous window closed is rejected without
// touching the session.
func (c *Coordinator) Join(rawCode string, client JoinClient) (JoinResult, error) {
	s, _, err := c.lookup(rawCode)
	if err != nil {
		return JoinResult{}, err
	}

	connID, err := randomToken(connectionIDLengthBytes)
	if err != nil {
		return JoinResult{}, fmt.Errorf("generate connection id: %w", err)
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return JoinResult{}, s.endedErrLocked()
	}
	if s.expiredLocked(now) {
		return JoinResult{}, ErrExpired
	}
	if _, declined := s.declinedClients[client.Key]; declined {
		return JoinResult{}, ErrDeclined
	}
	if s.selected != nil {
		return JoinResult{}, ErrAlreadyConnected
	}
	s.pruneLocked(now)
	pending := s.pendingLocked()
	if len(pending) >= MaxPendingConnections {
		return JoinResult{}, ErrRoomFull
	}
	fromNetwork := 0
	for _, conn := range pending {
		if conn.network == client.Network {
			fromNetwork++
		}
	}
	if fromNetwork >= MaxPendingPerNetwork {
		return JoinResult{}, ErrNetworkFull
	}

	verifyCode := ""
	if s.requireConfirmation {
		taken := make(map[string]bool, len(pending))
		for _, conn := range pending {
			taken[conn.verifyCode] = true
		}
		for verifyCode == "" || taken[verifyCode] {
			if verifyCode, err = generateVerifyCode(); err != nil {
				return JoinResult{}, err
			}
		}
	}

	s.nextIndex++
	conn := &connection{
		id:         connID,
		index:      s.nextIndex,
		verifyCode: verifyCode,
		device:     client.Device,
		clientKey:  client.Key,
		network:    client.Network,
		joinedAt:   now,
		lastSeen:   now,
		decided:    make(chan struct{}),
	}
	s.connections[connID] = conn
	if s.requireConfirmation {
		s.notifyLocked()
	}

	result := JoinResult{
		ConnectionID:        connID,
		RequireConfirmation: s.requireConfirmation,
		ExpiresIn:           max(0, int(s.expiresAt.Sub(now).Seconds())),
	}
	if s.requireConfirmation {
		result.VerifyCode = verifyCode
		result.Device = client.Device
	}
	return result, nil
}

// Status is a waiting receiver's long-poll (confirmation on only). It returns
// selected=true once the sender picked this connection (the receiver then
// opens Receive), an error once the connection was declined (ErrDeclined), not
// chosen (ErrNotSelected), or the session ended, and selected=false after
// maxWait while it is still waiting. An open Status call is what keeps a
// waiting connection in the room: a connection with no Status call for the
// presence timeout is dropped (Session.pruneLocked).
func (c *Coordinator) Status(ctx context.Context, rawCode, connID string, maxWait time.Duration) (bool, error) {
	s, _, err := c.lookup(rawCode)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	conn, ok := s.connections[connID]
	if ok && conn.isDecided() && conn.result != nil {
		// A connection the sender already settled keeps its own answer, even
		// after the session ends.
		s.mu.Unlock()
		return false, conn.result
	}
	if s.ended {
		cause := s.endedErrLocked()
		s.mu.Unlock()
		return false, cause
	}
	if !ok {
		s.mu.Unlock()
		return false, ErrNotFound
	}
	if !s.requireConfirmation {
		s.mu.Unlock()
		return false, ErrConfirmationDisabled
	}
	now := time.Now()
	if s.expiredLocked(now) {
		s.mu.Unlock()
		return false, ErrExpired
	}
	if conn.isDecided() {
		s.mu.Unlock()
		return true, nil
	}
	conn.polls++
	conn.lastSeen = now
	hold := min(maxWait, s.expiresAt.Sub(now))
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		conn.polls--
		conn.lastSeen = time.Now()
		s.mu.Unlock()
	}()

	timer := time.NewTimer(hold)
	defer timer.Stop()
	select {
	case <-conn.decided:
		if conn.result != nil {
			return false, conn.result
		}
		return true, nil
	case <-s.done:
		return false, c.endCause(s)
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.expiredLocked(time.Now()) {
			return false, ErrExpired
		}
		return false, nil
	}
}

// Wait returns the sender's view of the session as soon as its version differs
// from since, or after maxWait with the current view (long-poll).
func (c *Coordinator) Wait(ctx context.Context, rawCode, senderToken string, since uint64, maxWait time.Duration) (WaitSnapshot, error) {
	s, _, err := c.lookupSender(rawCode, senderToken)
	if err != nil {
		return WaitSnapshot{}, err
	}

	snapshot, changed, err := s.snapshot()
	if err != nil || snapshot.Version != since {
		return snapshot, err
	}

	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-changed:
	case <-s.done:
		return WaitSnapshot{}, c.endCause(s)
	case <-ctx.Done():
		return WaitSnapshot{}, ctx.Err()
	case <-timer.C:
	}
	// A fresh snapshot also prunes connections that went idle during the hold.
	snapshot, _, err = s.snapshot()
	return snapshot, err
}

func (s *Session) snapshot() (WaitSnapshot, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return WaitSnapshot{}, nil, s.endedErrLocked()
	}
	now := time.Now()
	if s.expiredLocked(now) {
		return WaitSnapshot{}, nil, ErrExpired
	}
	s.pruneLocked(now)
	snapshot := WaitSnapshot{Version: s.version, Pending: []PendingConnection{}}
	select {
	case <-s.receiverReady:
		snapshot.ReceiverReady = true
	default:
	}
	if s.requireConfirmation {
		for _, conn := range s.pendingLocked() {
			snapshot.Pending = append(snapshot.Pending, PendingConnection{
				ID:         conn.id,
				Index:      conn.index,
				VerifyCode: conn.verifyCode,
				Device:     conn.device,
				JoinedAt:   conn.joinedAt,
			})
		}
	}
	return snapshot, s.changed, nil
}

// Select hands the transfer to one undecided connection. Every other
// undecided connection ends with ErrNotSelected. A connection that already left
// the room (dropped as idle) or was already settled answers ErrNotPending.
func (c *Coordinator) Select(rawCode, senderToken, connID string) error {
	s, _, err := c.lookupSender(rawCode, senderToken)
	if err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s.endedErrLocked()
	}
	if s.expiredLocked(now) {
		return ErrExpired
	}
	if !s.requireConfirmation {
		return ErrConfirmationDisabled
	}
	if s.selected != nil {
		return ErrAlreadyConnected
	}
	s.pruneLocked(now)
	conn, ok := s.connections[connID]
	if !ok || conn.isDecided() {
		return ErrNotPending
	}
	s.selectLocked(conn, ErrNotSelected)
	return nil
}

// Decline ends one undecided connection with ErrDeclined. Its slot reopens
// and the session stays alive. The connection's client key joins the session's
// block list, so the same client cannot rejoin this session. The key is coarse
// by design: another client behind the same NAT with the same User-Agent is
// blocked too for this one short session, and a client that changes network or
// User-Agent gets through; the sender can decline it again. A connection that
// already left the room or was already settled answers ErrNotPending.
func (c *Coordinator) Decline(rawCode, senderToken, connID string) error {
	s, _, err := c.lookupSender(rawCode, senderToken)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s.endedErrLocked()
	}
	if !s.requireConfirmation {
		return ErrConfirmationDisabled
	}
	conn, ok := s.connections[connID]
	if !ok || conn.isDecided() {
		return ErrNotPending
	}
	conn.result = ErrDeclined
	close(conn.decided)
	s.declinedClients[conn.clientKey] = struct{}{}
	s.notifyLocked()
	return nil
}

// Cancel burns the whole session before streaming starts. Every waiting
// receiver ends with ErrCancelled. A transfer that is already streaming is
// never cut: Cancel returns ErrStreaming instead (the sender stops a stream by
// aborting its own send request). The streaming check and the end happen under
// one s.mu hold, so a stream that Send starts concurrently is never cut.
func (c *Coordinator) Cancel(rawCode, senderToken string) error {
	s, hash, err := c.lookupSender(rawCode, senderToken)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[hash] != s {
		return ErrNotFound
	}
	s.mu.Lock()
	if s.streaming {
		s.mu.Unlock()
		return ErrStreaming
	}
	cause := s.endLocked(ErrCancelled)
	s.mu.Unlock()
	c.dropLocked(hash, s, cause)
	return nil
}

// Receive returns the pipe for the chosen connection once the sender starts.
// With confirmation on, only the connection the sender selected may call it:
// a still-waiting one gets ErrAwaitingDecision (it keeps polling Status), and a
// declined or not-chosen one gets its own answer. With confirmation off the
// first connection to call Receive wins; later ones get ErrAlreadyConnected.
// A rejected receiver never counts against the session.
func (c *Coordinator) Receive(ctx context.Context, rawCode, connID string) (*io.PipeReader, []byte, SessionMetadata, func(), error) {
	s, hash, err := c.lookup(rawCode)
	if err != nil {
		return nil, nil, SessionMetadata{}, nil, err
	}

	s.mu.Lock()
	conn, ok := s.connections[connID]
	if ok && conn.isDecided() && conn.result != nil {
		// A connection the sender already settled keeps its own answer, even
		// after the session ends.
		s.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, conn.result
	}
	if s.ended {
		cause := s.endedErrLocked()
		s.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, cause
	}
	if !ok {
		s.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, ErrNotFound
	}
	if conn.attached {
		s.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, ErrAlreadyConnected
	}
	if s.requireConfirmation && s.selected != conn {
		s.mu.Unlock()
		return nil, nil, SessionMetadata{}, nil, ErrAwaitingDecision
	}
	if !s.requireConfirmation && s.selected == nil {
		s.selectLocked(conn, ErrAlreadyConnected)
	}
	conn.attached = true
	s.mu.Unlock()

	s.receiverOnce.Do(func() {
		close(s.receiverReady)
		s.mu.Lock()
		s.notifyLocked()
		s.mu.Unlock()
	})

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
		return nil, nil, SessionMetadata{}, nil, c.endCause(s)
	case <-ctx.Done():
		// The chosen receiver leaving before the stream starts ends the
		// session: nobody else can take over.
		cleanup()
		return nil, nil, SessionMetadata{}, nil, ctx.Err()
	case <-time.After(time.Until(s.expiresAt)):
		cleanup()
		return nil, nil, SessionMetadata{}, nil, ErrExpired
	}
}

func (c *Coordinator) endCause(s *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endedErrLocked()
}

// Send sets stream metadata, waits for the chosen receiver, and copies body to
// the pipe. Only the sender token holder can send.
func (c *Coordinator) Send(ctx context.Context, rawCode, senderToken string, meta SessionMetadata, body io.Reader) (int64, error) {
	s, hash, err := c.lookupSender(rawCode, senderToken)
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	if s.ended {
		cause := s.endedErrLocked()
		s.mu.Unlock()
		return 0, cause
	}
	s.kind = meta.Kind
	s.encryptedFilename = meta.EncryptedFilename
	s.mu.Unlock()

	alreadySending := false
	s.senderOnce.Do(func() {
		close(s.senderReady)
		alreadySending = true
	})

	if !alreadySending {
		return 0, ErrAlreadyConnected
	}

	select {
	case <-s.receiverReady:
	case <-s.done:
		return 0, c.endCause(s)
	case <-ctx.Done():
		_ = s.pipeWriter.CloseWithError(ctx.Err())
		c.burnSession(hash)
		return 0, ctx.Err()
	case <-time.After(time.Until(s.expiresAt)):
		_ = s.pipeWriter.CloseWithError(ErrExpired)
		c.burnSession(hash)
		return 0, ErrExpired
	}

	// The stream starts only if nothing ended the session first: Cancel and
	// cleanExpired check streaming and end the session under this same lock.
	s.mu.Lock()
	if s.ended {
		cause := s.endedErrLocked()
		s.mu.Unlock()
		return 0, cause
	}
	s.streaming = true
	s.streamStartedAt = time.Now().UTC()
	s.mu.Unlock()

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

// removeLocked ends a session unconditionally and wakes everyone blocked on it.
// Caller holds c.mu.
func (c *Coordinator) removeLocked(hash [32]byte, cause error) {
	s, ok := c.sessions[hash]
	if !ok {
		return
	}
	s.mu.Lock()
	cause = s.endLocked(cause)
	s.mu.Unlock()
	c.dropLocked(hash, s, cause)
}

// dropLocked deletes an ended session and closes its channels and pipe with
// cause. Caller holds c.mu and has already marked s ended with endLocked.
func (c *Coordinator) dropLocked(hash [32]byte, s *Session, cause error) {
	if c.sessions[hash] == s {
		delete(c.sessions, hash)
	}
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

// cleanExpired removes sessions past their rendezvous TTL and sessions whose
// chosen connection never opened Receive, and drops idle waiting connections
// from the rest. A session that is already streaming is exempt from the TTL and
// is only cut once it exceeds maxTransferDuration, so the 3-minute rendezvous
// window never truncates a transfer that started near its end. Each decision
// and its end happen under one s.mu hold (see Session.endLocked).
func (c *Coordinator) cleanExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for hash, s := range c.sessions {
		s.mu.Lock()
		var cause error
		switch {
		case s.streaming:
			if now.After(s.streamStartedAt.Add(c.maxTransferDuration)) {
				cause = ErrExpired
			}
		case now.After(s.expiresAt):
			cause = ErrExpired
		case s.chosenAbandonedLocked(now):
			// Same as the chosen receiver leaving before the stream starts.
			cause = ErrSessionClosed
		default:
			s.pruneLocked(now)
		}
		if cause == nil {
			s.mu.Unlock()
			continue
		}
		cause = s.endLocked(cause)
		s.mu.Unlock()
		c.dropLocked(hash, s, cause)
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
