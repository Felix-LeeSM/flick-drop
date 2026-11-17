package piperelay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrCapacityExceeded = errors.New("piperelay: maximum concurrent pipe sessions reached")
	ErrNotFound         = errors.New("piperelay: rendezvous session or connection not found or expired")
	ErrSessionClosed    = errors.New("piperelay: rendezvous session closed")
	ErrCancelled        = errors.New("piperelay: sender cancelled the rendezvous session")
	ErrAlreadyConnected = errors.New("piperelay: another receiver already holds the transfer")
	ErrRoomFull         = errors.New("piperelay: too many receivers are waiting on this session")
	ErrDeclined         = errors.New("piperelay: sender declined this connection")
	ErrNotSelected      = errors.New("piperelay: sender chose another connection")
	ErrNotPending       = errors.New("piperelay: connection is no longer waiting for a decision")
	ErrStreaming        = errors.New("piperelay: transfer is already streaming")
	ErrNetworkFull      = errors.New("piperelay: too many receivers from one client network are waiting on this session")
	ErrAwaitingDecision = errors.New("piperelay: connection is still waiting for the sender's pick")
	ErrPollInProgress   = errors.New("piperelay: connection already has the maximum open status polls")
	ErrInvalidCode      = errors.New("piperelay: code must be a 6-digit numeric string")
	ErrExpired          = errors.New("piperelay: rendezvous session expired")
	// ErrUnavailable wraps a Valkey failure. The HTTP layer answers 503, the
	// same as a server without Valkey.
	ErrUnavailable = errors.New("piperelay: live relay state store unavailable")
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
	defaultPresenceTimeout = 35 * time.Second
	defaultNamespace       = "pipe:"
	// endedRetention is how long an ended room stays in Valkey as a tombstone
	// (so a caller already in the session learns how it ended), and how long a
	// room key outlives its own deadline.
	endedRetention = time.Minute
	// resyncInterval bounds how long a waiter goes without re-reading its
	// room. Pub/Sub wakes it at once; this only covers a message lost while
	// the subscription reconnects.
	resyncInterval = 2 * time.Second
	// endRoomTimeout bounds the Valkey write that ends a room from a cleanup
	// path whose request context is already gone.
	endRoomTimeout          = 2 * time.Second
	tokenLengthBytes        = 32
	connectionIDLengthBytes = 16
	// MaxPendingConnections caps the receivers one session holds undecided at
	// once. The next joiner gets ErrRoomFull; the session itself stays alive.
	MaxPendingConnections = 10
	// MaxPendingPerNetwork caps the undecided receivers from one client network
	// (an IPv4 address or an IPv6 /64, derived by the HTTP layer) in one
	// session, so one network cannot fill the room alone.
	MaxPendingPerNetwork = 3
	// MaxStatusPollsPerConnection caps the concurrent Status calls of one
	// connection. Two leave room for a client whose previous poll has not
	// finished closing yet; the next one gets ErrPollInProgress, so one
	// connection ID cannot pile up goroutines and sockets.
	MaxStatusPollsPerConnection = 2
)

// SessionMetadata carries lightweight, non-secret file/text descriptor headers.
// It travels only on the pod-to-pod stream request, never through Valkey.
type SessionMetadata struct {
	Kind              string // e.g. "text" or "file"
	EncryptedFilename string // base64-encoded encrypted filename (if file)
}

// InitResult is what the sender gets back from Init. SenderToken is the only
// credential for the sender-side calls (Wait, Select, Decline, Cancel, Send).
type InitResult struct {
	Code        string
	SenderToken string
	ExpiresIn   int
}

// JoinResult is what a receiver gets back from Join. VerifyCode is its unique
// 3-digit confirmation number to read to the sender.
type JoinResult struct {
	ConnectionID string
	VerifyCode   string
	Device       string
	// ExpiresIn is the seconds left in the rendezvous window.
	ExpiresIn int
}

// PendingConnection is one undecided receiver as the sender sees it.
type PendingConnection struct {
	ID         string
	Index      int
	VerifyCode string
	Device     string
	// Recipient is the age public key this receiver generated in its browser;
	// the sender encrypts to it when it picks this connection.
	Recipient string
	JoinedAt  time.Time
}

// WaitSnapshot is the sender's view of a session at Version.
type WaitSnapshot struct {
	Version uint64
	// ReceiverReady is true once the chosen receiver is attached and Send can stream.
	ReceiverReady bool
	// Pending lists undecided receivers in join order.
	Pending []PendingConnection
	// Recipient is the attached receiver's age public key once ReceiverReady
	// is true, and empty before.
	Recipient string
}

// ClientKey identifies a receiving client for the per-session decline block.
// The HTTP layer derives it as a hash of the client IP and User-Agent; this
// package never sees either value, and stores it in Valkey only after a
// second HMAC under CoordinatorOptions.KeySalt.
type ClientKey [32]byte

// ClientNetwork identifies a receiving client's network (IPv4 address or IPv6
// /64) for the MaxPendingPerNetwork cap. The HTTP layer derives it as a hash;
// this package never sees the address, and stores it in Valkey only after a
// second HMAC under CoordinatorOptions.KeySalt.
type ClientNetwork [32]byte

// JoinClient is what Join knows about a receiving client.
type JoinClient struct {
	// Device is the coarse label shown to the sender, such as "Safari on iPhone".
	Device string
	// Recipient is the age public key the receiver generated in its browser
	// (the HTTP layer validates its shape). The coordinator only stores it and
	// hands it to the sender; the matching identity never leaves the browser.
	Recipient string
	Key       ClientKey
	Network   ClientNetwork
}

// CoordinatorOptions configures the coordinator.
type CoordinatorOptions struct {
	// Valkey holds every rendezvous room, shared by all API replicas.
	Valkey *redis.Client
	// KeySalt is the HMAC-SHA-256 key for room IDs and client hashes in
	// Valkey (FLICK_RATELIMIT_KEY_SALT).
	KeySalt string
	// InternalToken authenticates the pod-to-pod stream request
	// (FLICK_INTERNAL_TOKEN).
	InternalToken string
	// AdvertiseIP is the IP literal the sender's pod dials to reach this
	// pod's stream listener (FLICK_POD_IP; 127.0.0.1 outside Kubernetes).
	AdvertiseIP netip.Addr
	// InternalPort is the stream listener port, the same on every pod
	// (FLICK_INTERNAL_HTTP_PORT).
	InternalPort uint16
	// StreamTransport dials peer pods. Nil uses a direct transport that never
	// goes through an HTTP proxy. Tests map fake pod IPs to local listeners.
	StreamTransport http.RoundTripper
	// Namespace prefixes every Valkey key; defaultNamespace ("pipe:") unless
	// a test isolates itself.
	Namespace           string
	MaxConcurrent       int
	TTL                 time.Duration
	CleanInterval       time.Duration
	MaxTransferDuration time.Duration
	// PresenceTimeout defaults to defaultPresenceTimeout (35 seconds).
	PresenceTimeout time.Duration
}

// Coordinator runs live relay rendezvous across API replicas. Room state
// lives in Valkey (room.go) so any replica answers any call; the ciphertext
// itself goes from the sender's replica straight to the receiver's replica
// over HTTP (stream.go) and never enters Valkey.
type Coordinator struct {
	valkey              *redis.Client
	keySalt             []byte
	internalToken       string
	advertiseIP         netip.Addr
	internalPort        uint16
	streamClient        *http.Client
	namespace           string
	maxConcurrent       int
	ttl                 time.Duration
	cleanInterval       time.Duration
	maxTransferDuration time.Duration
	presenceTimeout     time.Duration

	hub    roomHub
	pubsub *redis.PubSub

	slotsMu sync.Mutex
	slots   map[roomID]*streamSlot

	drainOnce sync.Once
	draining  chan struct{}
	closeOnce sync.Once
	closed    chan struct{}
	workers   sync.WaitGroup
}

// NewCoordinator validates opts, subscribes to room events, and starts the
// background cleaner.
func NewCoordinator(opts CoordinatorOptions) (*Coordinator, error) {
	switch {
	case opts.Valkey == nil:
		return nil, errors.New("piperelay: Valkey client is required")
	case opts.KeySalt == "":
		return nil, errors.New("piperelay: key salt is required")
	case opts.InternalToken == "":
		return nil, errors.New("piperelay: internal token is required for the pod-to-pod stream")
	case !opts.AdvertiseIP.IsValid():
		return nil, errors.New("piperelay: advertise IP is required")
	case opts.InternalPort == 0:
		return nil, errors.New("piperelay: internal port is required")
	}
	transport := opts.StreamTransport
	if transport == nil {
		transport = newStreamTransport()
	}
	c := &Coordinator{
		valkey:        opts.Valkey,
		keySalt:       []byte(opts.KeySalt),
		internalToken: opts.InternalToken,
		advertiseIP:   opts.AdvertiseIP.Unmap(),
		internalPort:  opts.InternalPort,
		streamClient: &http.Client{
			Transport: transport,
			// The stream target comes from Valkey; never follow it elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		namespace:           orDefault(opts.Namespace, defaultNamespace),
		maxConcurrent:       orDefault(opts.MaxConcurrent, defaultMaxConcurrent),
		ttl:                 orDefault(opts.TTL, defaultTTL),
		cleanInterval:       orDefault(opts.CleanInterval, defaultCleanInterval),
		maxTransferDuration: orDefault(opts.MaxTransferDuration, defaultMaxTransferDuration),
		presenceTimeout:     orDefault(opts.PresenceTimeout, defaultPresenceTimeout),
		slots:               map[roomID]*streamSlot{},
		draining:            make(chan struct{}),
		closed:              make(chan struct{}),
	}

	// One pattern subscription per process; go-redis reconnects it (and
	// re-subscribes) on its own when Valkey restarts.
	c.pubsub = c.valkey.PSubscribe(context.Background(), c.namespace+"ev:*")
	events := c.pubsub.Channel()
	c.workers.Add(2)
	go func() {
		defer c.workers.Done()
		for msg := range events {
			c.hub.fire(roomID(strings.TrimPrefix(msg.Channel, c.namespace+"ev:")))
		}
	}()
	go c.runCleaner()
	return c, nil
}

func orDefault[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
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
// code and the sender token. The concurrent-session cap counts rooms across
// every replica.
func (c *Coordinator) Init(ctx context.Context) (InitResult, error) {
	senderToken, err := randomToken(tokenLengthBytes)
	if err != nil {
		return InitResult{}, fmt.Errorf("generate sender token: %w", err)
	}
	now := time.Now()
	r := room{
		SenderTokenHash: sha256Hex(senderToken),
		ExpiresAt:       now.Add(c.ttl),
		Version:         1,
		Connections:     []roomConn{},
		DeclinedClients: []string{},
	}
	state, err := json.Marshal(r)
	if err != nil {
		return InitResult{}, fmt.Errorf("piperelay: encode room state: %w", err)
	}

	for range 10 {
		code, err := GenerateCode()
		if err != nil {
			return InitResult{}, err
		}
		id, _ := c.roomIDFor(code)
		created, err := createRoomScript.Run(ctx, c.valkey,
			[]string{c.roomKey(id), c.roomsKey()},
			string(id), now.UnixMilli(), c.maxConcurrent, state,
			(c.ttl + endedRetention).Milliseconds(), r.ExpiresAt.UnixMilli(),
		).Int()
		if err != nil {
			return InitResult{}, unavailable(err)
		}
		switch created {
		case -1:
			return InitResult{}, ErrCapacityExceeded
		case 1:
			return InitResult{Code: code, SenderToken: senderToken, ExpiresIn: int(c.ttl.Seconds())}, nil
		}
	}
	return InitResult{}, errors.New("piperelay: failed to generate unique code")
}

// Join admits a receiver that entered the right code and returns its
// connection with its own random 3-digit verification number, unique among the
// session's undecided connections. A late joiner (transfer already claimed), an
// 11th waiting joiner, a 4th waiting joiner from one client network, a client
// the sender already declined, or a join after the rendezvous window closed is
// rejected without touching the session.
func (c *Coordinator) Join(ctx context.Context, rawCode string, client JoinClient) (JoinResult, error) {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return JoinResult{}, err
	}
	current, err := c.view(ctx, id)
	if err != nil {
		return JoinResult{}, err
	}
	if _, err := c.pruneIdle(ctx, id, current); err != nil {
		return JoinResult{}, err
	}

	connID, err := randomToken(connectionIDLengthBytes)
	if err != nil {
		return JoinResult{}, fmt.Errorf("generate connection id: %w", err)
	}
	// The lease exists before the connection does, so a concurrent prune
	// never drops a connection that just joined.
	lease := c.leaseKey(id, connID)
	if err := c.valkey.Set(ctx, lease, 0, c.presenceTimeout).Err(); err != nil {
		return JoinResult{}, unavailable(err)
	}

	key := c.mac([]byte("client"), client.Key[:])
	network := c.mac([]byte("network"), client.Network[:])
	var result JoinResult
	_, err = c.update(ctx, id, func(r *room, now time.Time) (bool, error) {
		if r.ended() {
			return false, r.cause()
		}
		for _, declined := range r.DeclinedClients {
			if declined == key {
				return false, ErrDeclined
			}
		}
		if r.Selected != "" {
			return false, ErrAlreadyConnected
		}
		pending := r.pending()
		if len(pending) >= MaxPendingConnections {
			return false, ErrRoomFull
		}
		fromNetwork := 0
		taken := make(map[string]bool, len(pending))
		for _, conn := range pending {
			if conn.Network == network {
				fromNetwork++
			}
			taken[conn.VerifyCode] = true
		}
		if fromNetwork >= MaxPendingPerNetwork {
			return false, ErrNetworkFull
		}
		verifyCode := ""
		for verifyCode == "" || taken[verifyCode] {
			code, err := generateVerifyCode()
			if err != nil {
				return false, err
			}
			verifyCode = code
		}
		r.NextIndex++
		r.Connections = append(r.Connections, roomConn{
			ID:         connID,
			Index:      r.NextIndex,
			VerifyCode: verifyCode,
			Device:     client.Device,
			Recipient:  client.Recipient,
			ClientKey:  key,
			Network:    network,
			JoinedAt:   now,
		})
		r.Version++
		result = JoinResult{
			ConnectionID: connID,
			VerifyCode:   verifyCode,
			Device:       client.Device,
			ExpiresIn:    max(0, int(r.ExpiresAt.Sub(now).Seconds())),
		}
		return true, nil
	})
	if err != nil {
		_ = c.valkey.Del(context.WithoutCancel(ctx), lease).Err()
		return JoinResult{}, err
	}
	return result, nil
}

// statusAnswer is Status's answer for the room as it stands; done is false
// while the connection still waits for the sender.
func statusAnswer(r *room, connID string) (done, selected bool, err error) {
	conn := r.conn(connID)
	if conn != nil {
		if result := conn.loserResult(); result != nil {
			// A connection the sender already settled keeps its own answer,
			// even after the session ends.
			return true, false, result
		}
	}
	switch {
	case r.ended():
		return true, false, r.cause()
	case conn == nil:
		return true, false, ErrNotFound
	case conn.Result == resultSelected:
		return true, true, nil
	}
	return false, false, nil
}

// Status is a waiting receiver's long-poll. It returns selected=true once the
// sender picked this connection (the receiver then opens Receive), an error
// once the connection was declined (ErrDeclined), not chosen (ErrNotSelected),
// or the session ended, and selected=false after maxWait while it is still
// waiting (or at once while the coordinator drains for shutdown). An open
// Status call is what keeps a waiting connection in the room: its poll lease
// expires the presence timeout after the last poll started or ended.
func (c *Coordinator) Status(ctx context.Context, rawCode, connID string, maxWait time.Duration) (bool, error) {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return false, err
	}
	changed, unwatch := c.hub.watch(id)
	defer unwatch()

	r, err := c.view(ctx, id)
	if err != nil {
		return false, err
	}
	if done, selected, err := statusAnswer(&r, connID); done {
		return selected, err
	}

	lease := c.leaseKey(id, connID)
	opened, err := acquireLeaseScript.Run(ctx, c.valkey, []string{lease}, MaxStatusPollsPerConnection, c.presenceTimeout.Milliseconds()).Int()
	if err != nil {
		return false, unavailable(err)
	}
	if opened == 0 {
		return false, ErrPollInProgress
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endRoomTimeout)
		defer cancel()
		_ = releaseLeaseScript.Run(releaseCtx, c.valkey, []string{lease}, c.presenceTimeout.Milliseconds()).Err()
	}()

	hold := time.NewTimer(min(maxWait, time.Until(r.ExpiresAt)))
	defer hold.Stop()
	resync := time.NewTicker(resyncInterval)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-c.draining:
			return false, nil
		case <-hold.C:
			r, err := c.view(ctx, id)
			if err != nil {
				return false, err
			}
			_, selected, err := statusAnswer(&r, connID)
			return selected, err
		case <-changed:
		case <-resync.C:
		}
		r, err := c.view(ctx, id)
		if err != nil {
			return false, err
		}
		if done, selected, err := statusAnswer(&r, connID); done {
			return selected, err
		}
	}
}

// senderView reads the room for a sender call. A wrong token answers exactly
// like an unknown code, so sender endpoints are no code oracle.
func (c *Coordinator) senderView(ctx context.Context, id roomID, senderToken string) (room, error) {
	r, err := c.view(ctx, id)
	if err != nil {
		return room{}, err
	}
	if !r.senderMatches(senderToken) {
		return room{}, ErrNotFound
	}
	if r.ended() {
		return room{}, r.cause()
	}
	return r, nil
}

// Wait returns the sender's view of the session as soon as its version differs
// from since, or after maxWait with the current view (long-poll). While the
// coordinator drains for shutdown it answers at once.
func (c *Coordinator) Wait(ctx context.Context, rawCode, senderToken string, since uint64, maxWait time.Duration) (WaitSnapshot, error) {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return WaitSnapshot{}, err
	}
	changed, unwatch := c.hub.watch(id)
	defer unwatch()

	snapshot := func() (WaitSnapshot, error) {
		r, err := c.senderView(ctx, id, senderToken)
		if err != nil {
			return WaitSnapshot{}, err
		}
		if r, err = c.pruneIdle(ctx, id, r); err != nil {
			return WaitSnapshot{}, err
		}
		if r.ended() {
			return WaitSnapshot{}, r.cause()
		}
		return snapshotOf(&r), nil
	}

	snap, err := snapshot()
	if err != nil || snap.Version != since {
		return snap, err
	}
	hold := time.NewTimer(maxWait)
	defer hold.Stop()
	resync := time.NewTicker(resyncInterval)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return WaitSnapshot{}, ctx.Err()
		case <-c.draining:
			return snapshot()
		case <-hold.C:
			// A fresh snapshot also prunes connections that went idle during the hold.
			return snapshot()
		case <-changed:
		case <-resync.C:
		}
		snap, err := snapshot()
		if err != nil || snap.Version != since {
			return snap, err
		}
	}
}

func snapshotOf(r *room) WaitSnapshot {
	snap := WaitSnapshot{Version: r.Version, Pending: []PendingConnection{}}
	if r.receiverReady() {
		snap.ReceiverReady = true
		snap.Recipient = r.conn(r.Selected).Recipient
	}
	for _, conn := range r.pending() {
		snap.Pending = append(snap.Pending, PendingConnection{
			ID:         conn.ID,
			Index:      conn.Index,
			VerifyCode: conn.VerifyCode,
			Device:     conn.Device,
			Recipient:  conn.Recipient,
			JoinedAt:   conn.JoinedAt,
		})
	}
	return snap
}

// Select hands the transfer to one undecided connection. Every other
// undecided connection ends with ErrNotSelected. A connection that already left
// the room (dropped as idle) or was already settled answers ErrNotPending.
func (c *Coordinator) Select(ctx context.Context, rawCode, senderToken, connID string) error {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return err
	}
	r, err := c.senderView(ctx, id, senderToken)
	if err != nil {
		return err
	}
	if _, err := c.pruneIdle(ctx, id, r); err != nil {
		return err
	}
	_, err = c.update(ctx, id, func(r *room, now time.Time) (bool, error) {
		if !r.senderMatches(senderToken) {
			return false, ErrNotFound
		}
		if r.ended() {
			return false, r.cause()
		}
		if r.Selected != "" {
			return false, ErrAlreadyConnected
		}
		chosen := r.conn(connID)
		if chosen == nil || chosen.Result != "" {
			return false, ErrNotPending
		}
		for _, conn := range r.pending() {
			conn.Result = resultNotSelected
		}
		chosen.Result = resultSelected
		r.Selected = connID
		r.SelectedAt = now
		r.Version++
		return true, nil
	})
	return err
}

// Decline ends one undecided connection with ErrDeclined. Its slot reopens
// and the session stays alive. The connection's client key joins the session's
// block list, so the same client cannot rejoin this session. The key is coarse
// by design: another client behind the same NAT with the same User-Agent is
// blocked too for this one short session, and a client that changes network or
// User-Agent gets through; the sender can decline it again. A connection that
// already left the room or was already settled answers ErrNotPending.
func (c *Coordinator) Decline(ctx context.Context, rawCode, senderToken, connID string) error {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return err
	}
	_, err = c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		if !r.senderMatches(senderToken) {
			return false, ErrNotFound
		}
		if r.ended() {
			return false, r.cause()
		}
		conn := r.conn(connID)
		if conn == nil || conn.Result != "" {
			return false, ErrNotPending
		}
		conn.Result = resultDeclined
		r.DeclinedClients = append(r.DeclinedClients, conn.ClientKey)
		r.Version++
		return true, nil
	})
	return err
}

// Cancel burns the whole session before streaming starts. Every waiting
// receiver ends with ErrCancelled. A transfer that is already streaming is
// never cut: Cancel returns ErrStreaming instead (the sender stops a stream by
// aborting its own send request). Cancel and Send's start of streaming are
// both compare-and-set writes on the same room, so exactly one of them wins.
// A session that already ended answers ErrNotFound.
func (c *Coordinator) Cancel(ctx context.Context, rawCode, senderToken string) error {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return err
	}
	_, err = c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		if !r.senderMatches(senderToken) || r.ended() {
			return false, ErrNotFound
		}
		if r.streaming() {
			return false, ErrStreaming
		}
		r.end(causeCancelled)
		return true, nil
	})
	return err
}

// ActiveSessions counts the live rendezvous sessions across every replica
// (for tests and telemetry).
func (c *Coordinator) ActiveSessions(ctx context.Context) (int, error) {
	n, err := c.valkey.ZCount(ctx, c.roomsKey(), "("+strconv.FormatInt(time.Now().UnixMilli(), 10), "+inf").Result()
	if err != nil {
		return 0, unavailable(err)
	}
	return int(n), nil
}

// Drain makes every open Status and Wait long-poll on this replica answer at
// once with its current view, and every later one answer without holding. The
// API calls it on SIGTERM, so waiting browsers re-poll through another replica
// instead of being cut when this one stops; Send and Receive keep running.
func (c *Coordinator) Drain() {
	c.drainOnce.Do(func() { close(c.draining) })
}

func (c *Coordinator) runCleaner() {
	defer c.workers.Done()
	ticker := time.NewTicker(c.cleanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			c.cleanOnce()
		}
	}
}

// cleanOnce applies the lazy end rules (room.go settle) and idle pruning to
// every live room, so a room nobody touches still ends on time and frees its
// slot in the concurrent-session cap. Every replica runs it; the
// compare-and-set writes make concurrent cleaners harmless.
func (c *Coordinator) cleanOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), c.cleanInterval)
	defer cancel()
	ids, err := c.valkey.ZRange(ctx, c.roomsKey(), 0, -1).Result()
	if err != nil {
		slog.Warn("pipe relay cleaner could not list rooms", "error", err)
		return
	}
	for _, raw := range ids {
		id := roomID(raw)
		r, err := c.view(ctx, id)
		if errors.Is(err, ErrNotFound) {
			_ = c.valkey.ZRem(ctx, c.roomsKey(), raw).Err()
			continue
		}
		if err != nil {
			slog.Warn("pipe relay cleaner could not read a room", "error", err)
			continue
		}
		if _, err := c.pruneIdle(ctx, id, r); err != nil {
			slog.Warn("pipe relay cleaner could not prune a room", "error", err)
		}
	}
}

// Close stops the cleaner and the event subscription and releases every local
// waiter. It leaves rooms in Valkey untouched: another replica keeps serving
// them. No goroutine started by the coordinator survives Close.
func (c *Coordinator) Close() error {
	c.closeOnce.Do(func() {
		c.Drain()
		close(c.closed)
		_ = c.pubsub.Close()
		c.workers.Wait()
	})
	return nil
}

// errReceiverGone is the internal answer when the chosen receiver's request
// on this replica is no longer waiting for the stream.
var errReceiverGone = errors.New("piperelay: receiver is not waiting on this replica")

// copyBufferBytes matches the age streaming chunk size.
const copyBufferBytes = 64 * 1024

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufferBytes)
		return &b
	},
}

// copyWithPool copies src to dst through a pooled 64 KiB buffer and clears
// the buffer before returning it, so no ciphertext lingers in the pool.
func copyWithPool(dst io.Writer, src io.Reader) (int64, error) {
	ptr := bufferPool.Get().(*[]byte)
	buf := *ptr
	defer func() {
		clear(buf)
		bufferPool.Put(ptr)
	}()
	return io.CopyBuffer(dst, src, buf)
}
