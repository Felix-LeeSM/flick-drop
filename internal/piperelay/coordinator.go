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
	// The HTTP layer answers 503, the same as a server without Valkey.
	ErrUnavailable = errors.New("piperelay: live relay state store unavailable")
)

const (
	defaultMaxConcurrent       = 100
	defaultTTL                 = 3 * time.Minute
	defaultCleanInterval       = 30 * time.Second
	defaultMaxTransferDuration = 10 * time.Minute
	// Idle time without a Status poll, and the chosen connection's deadline to open Receive.
	// Must exceed the 25s Status hold (internal/httpapi/piperelay.go pipeWaitMaxHold).
	defaultPresenceTimeout = 35 * time.Second
	defaultNamespace       = "pipe:"
	// Ended-room tombstone lifetime, and how long a room key outlives its deadline.
	endedRetention = time.Minute
	// Covers a Pub/Sub message lost while the subscription reconnects.
	resyncInterval = 2 * time.Second
	// For cleanup paths whose request context is already gone.
	endRoomTimeout          = 2 * time.Second
	tokenLengthBytes        = 32
	connectionIDLengthBytes = 16
	// The next joiner gets ErrRoomFull; the session stays alive.
	MaxPendingConnections = 10
	// Per client network (IPv4 address or IPv6 /64), so one network cannot fill the room.
	MaxPendingPerNetwork = 3
	// Two leave room for a previous poll still closing; caps goroutines and sockets per connection ID.
	MaxStatusPollsPerConnection = 2
)

// Non-secret; travels only on the pod-to-pod stream request, never through Valkey.
type SessionMetadata struct {
	Kind              string // e.g. "text" or "file"
	EncryptedFilename string
}

// SenderToken is the only credential for sender-side calls.
type InitResult struct {
	Code        string
	SenderToken string
	ExpiresIn   int
}

type JoinResult struct {
	ConnectionID string
	VerifyCode   string
	Device       string
	ExpiresIn    int
}

type PendingConnection struct {
	ID         string
	Index      int
	VerifyCode string
	Device     string
	Recipient  string
	JoinedAt   time.Time
}

type WaitSnapshot struct {
	Version       uint64
	ReceiverReady bool
	// In join order.
	Pending   []PendingConnection
	Recipient string
}

// A hash of client IP and User-Agent from the HTTP layer; stored in Valkey only after a second HMAC under KeySalt.
type ClientKey [32]byte

// A hash of the IPv4 address or IPv6 /64; stored in Valkey only after a second HMAC under KeySalt.
type ClientNetwork [32]byte

type JoinClient struct {
	Device string
	// The matching identity never leaves the browser.
	Recipient string
	Key       ClientKey
	Network   ClientNetwork
}

type CoordinatorOptions struct {
	Valkey *redis.Client
	// HMAC-SHA-256 key for room IDs and client hashes in Valkey (FLICK_RATELIMIT_KEY_SALT).
	KeySalt string
	// FLICK_INTERNAL_TOKEN.
	InternalToken string
	// FLICK_POD_IP; 127.0.0.1 outside Kubernetes.
	AdvertiseIP netip.Addr
	// FLICK_INTERNAL_HTTP_PORT, the same on every pod.
	InternalPort uint16
	// Nil uses a direct transport that never goes through an HTTP proxy.
	StreamTransport     http.RoundTripper
	Namespace           string
	MaxConcurrent       int
	TTL                 time.Duration
	CleanInterval       time.Duration
	MaxTransferDuration time.Duration
	PresenceTimeout     time.Duration
}

// Room state lives in Valkey so any replica answers any call; ciphertext goes replica to replica
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

	// go-redis re-subscribes on its own when Valkey restarts.
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

func GenerateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", fmt.Errorf("generate random code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()+100000), nil
}

// Independent of the 6-digit code by construction.
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

// Rejects late joiners, a full room, a full network, declined clients, and joins after the window,
// all without touching the session.
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
	// The lease exists before the connection, so a concurrent prune never drops a just-joined connection.
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

func statusAnswer(r *room, connID string) (done, selected bool, err error) {
	conn := r.conn(connID)
	if conn != nil {
		if result := conn.loserResult(); result != nil {
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

// An open Status call keeps a waiting connection in the room: its poll lease expires the presence
// timeout after the last poll started or ended.
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

	pollID, err := randomToken(connectionIDLengthBytes)
	if err != nil {
		return false, fmt.Errorf("generate poll id: %w", err)
	}
	holdFor := min(maxWait, time.Until(r.ExpiresAt))
	leaseKeys := []string{c.leaseKey(id, connID), c.pollsKey(id, connID)}
	opened, err := acquireLeaseScript.Run(ctx, c.valkey, leaseKeys, MaxStatusPollsPerConnection,
		c.presenceTimeout.Milliseconds(), pollID, (max(holdFor, 0) + c.presenceTimeout).Milliseconds()).Int()
	if err != nil {
		return false, unavailable(err)
	}
	if opened == 0 {
		return false, ErrPollInProgress
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endRoomTimeout)
		defer cancel()
		_ = releaseLeaseScript.Run(releaseCtx, c.valkey, leaseKeys, c.presenceTimeout.Milliseconds(), pollID).Err()
	}()

	hold := time.NewTimer(holdFor)
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

// A wrong token answers like an unknown code, so sender endpoints are no code oracle.
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

// The client key is coarse by design: the same NAT and User-Agent is blocked too, for one short session.
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

// A streaming transfer is never cut. Cancel and Send's stream start are compare-and-set writes on the
// same room, so exactly one wins.
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

func (c *Coordinator) ActiveSessions(ctx context.Context) (int, error) {
	n, err := c.valkey.ZCount(ctx, c.roomsKey(), "("+strconv.FormatInt(time.Now().UnixMilli(), 10), "+inf").Result()
	if err != nil {
		return 0, unavailable(err)
	}
	return int(n), nil
}

// Called on SIGTERM so waiting browsers re-poll another replica; Send and Receive keep running.
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

// Every replica runs it; compare-and-set writes make concurrent cleaners harmless.
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

// Leaves rooms in Valkey for other replicas.
func (c *Coordinator) Close() error {
	c.closeOnce.Do(func() {
		c.Drain()
		close(c.closed)
		_ = c.pubsub.Close()
		c.workers.Wait()
	})
	return nil
}

var errReceiverGone = errors.New("piperelay: receiver is not waiting on this replica")

// Matches the age streaming chunk size.
const copyBufferBytes = 64 * 1024

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufferBytes)
		return &b
	},
}

// Clears the buffer before returning it, so no ciphertext lingers in the pool.
func copyWithPool(dst io.Writer, src io.Reader) (int64, error) {
	ptr := bufferPool.Get().(*[]byte)
	buf := *ptr
	defer func() {
		clear(buf)
		bufferPool.Put(ptr)
	}()
	return io.CopyBuffer(dst, src, buf)
}
