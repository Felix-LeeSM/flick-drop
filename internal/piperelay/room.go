package piperelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Hex HMAC-SHA-256 of the code under KeySalt: a bare SHA-256 of 900,000 codes would reveal the code in a key dump.
type roomID string

func validRoomID(raw string) bool {
	if len(raw) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

const (
	resultSelected    = "selected"
	resultDeclined    = "declined"
	resultNotSelected = "not_selected"
)

const (
	causeCancelled = "cancelled"
	causeExpired   = "expired"
	causeClosed    = "closed"
)

// Never holds ciphertext, the plaintext code, the sender token, or the stream secret: only their hashes.
type room struct {
	SenderTokenHash string    `json:"sender_token_hash"`
	ExpiresAt       time.Time `json:"expires_at"`
	// Moves only on changes the sender sees (joins, decisions, receiver ready, pruning).
	Version     uint64     `json:"version"`
	NextIndex   int        `json:"next_index"`
	Connections []roomConn `json:"connections"`
	// HMAC'd client keys.
	DeclinedClients []string  `json:"declined_clients"`
	Selected        string    `json:"selected,omitempty"`
	SelectedAt      time.Time `json:"selected_at,omitzero"`
	SenderClaimed   bool      `json:"sender_claimed,omitempty"`
	// The sender's pod dials it on CoordinatorOptions.InternalPort.
	ReceiverIP       string    `json:"receiver_ip,omitempty"`
	StreamSecretHash string    `json:"stream_secret_hash,omitempty"`
	StreamStartedAt  time.Time `json:"stream_started_at,omitzero"`
	EndCause         string    `json:"end_cause,omitempty"`
}

type roomConn struct {
	ID         string    `json:"id"`
	Index      int       `json:"index"`
	VerifyCode string    `json:"verify_code"`
	Device     string    `json:"device"`
	Recipient  string    `json:"recipient"`
	ClientKey  string    `json:"client_key"`
	Network    string    `json:"network"`
	JoinedAt   time.Time `json:"joined_at"`
	Attached   bool      `json:"attached,omitempty"`
	// Empty while the connection waits for the sender's pick.
	Result string `json:"result,omitempty"`
}

func (r *room) ended() bool     { return r.EndCause != "" }
func (r *room) streaming() bool { return !r.StreamStartedAt.IsZero() }

// The first cause wins.
func (r *room) end(cause string) {
	if r.EndCause == "" {
		r.EndCause = cause
	}
}

func (r *room) cause() error {
	switch r.EndCause {
	case causeCancelled:
		return ErrCancelled
	case causeExpired:
		return ErrExpired
	default:
		return ErrSessionClosed
	}
}

func (r *room) conn(id string) *roomConn {
	if id == "" {
		return nil
	}
	for i := range r.Connections {
		if r.Connections[i].ID == id {
			return &r.Connections[i]
		}
	}
	return nil
}

func (r *room) pending() []*roomConn {
	out := make([]*roomConn, 0, len(r.Connections))
	for i := range r.Connections {
		if r.Connections[i].Result == "" {
			out = append(out, &r.Connections[i])
		}
	}
	return out
}

func (r *room) receiverReady() bool {
	sel := r.conn(r.Selected)
	return sel != nil && sel.Attached
}

func (c *roomConn) loserResult() error {
	switch c.Result {
	case resultDeclined:
		return ErrDeclined
	case resultNotSelected:
		return ErrNotSelected
	default:
		return nil
	}
}

func (r *room) senderMatches(token string) bool {
	if token == "" {
		return false
	}
	given := sha256Hex(token)
	return subtle.ConstantTimeCompare([]byte(given), []byte(r.SenderTokenHash)) == 1
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (c *Coordinator) mac(parts ...[]byte) string {
	m := hmac.New(sha256.New, c.keySalt)
	for _, p := range parts {
		m.Write(p)
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (c *Coordinator) roomIDFor(rawCode string) (roomID, error) {
	code, err := NormalizeCode(rawCode)
	if err != nil {
		return "", err
	}
	return roomID(c.mac([]byte("code"), []byte(code))), nil
}

func (c *Coordinator) roomKey(id roomID) string      { return c.namespace + "room:" + string(id) }
func (c *Coordinator) roomsKey() string              { return c.namespace + "rooms" }
func (c *Coordinator) eventChannel(id roomID) string { return c.namespace + "ev:" + string(id) }

// TTL is the presence timeout, refreshed when a Status poll starts and ends.
func (c *Coordinator) leaseKey(id roomID, connID string) string {
	return c.namespace + "poll:" + string(id) + ":" + sha256Hex(connID)[:32]
}

// Members scored by when the poll stops counting, so a replica that dies mid-poll still frees the slot.
func (c *Coordinator) pollsKey(id roomID, connID string) string {
	return c.namespace + "polls:" + string(id) + ":" + sha256Hex(connID)[:32]
}

// Returns -1 at the room cap, 0 when the code is taken (tombstones included).
// KEYS: room, rooms. ARGV: roomID, now ms, max rooms, state, key TTL ms, rooms score (expiry ms).
var createRoomScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[2])
if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[3]) then return -1 end
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'rev', 1, 'state', ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
redis.call('ZADD', KEYS[2], ARGV[6], ARGV[1])
return 1
`)

// Writes only if rev is unchanged and publishes in the same atomic step. Score "-" removes from the live set; "" keeps it.
// KEYS: room, rooms, event channel. ARGV: expected rev, state, key TTL ms, roomID, rooms score.
var casRoomScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'rev') ~= ARGV[1] then return 0 end
local rev = tonumber(ARGV[1]) + 1
redis.call('HSET', KEYS[1], 'rev', rev, 'state', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
if ARGV[5] == '-' then
  redis.call('ZREM', KEYS[2], ARGV[4])
elseif ARGV[5] ~= '' then
  redis.call('ZADD', KEYS[2], 'XX', ARGV[5], ARGV[4])
end
redis.call('PUBLISH', KEYS[3], rev)
return 1
`)

// Returns 0 when ARGV[1] polls still count. Uses Valkey's clock so skewed replicas agree.
// KEYS: lease, polls. ARGV: max polls, lease TTL ms (hold + presence), poll member, poll lifetime ms.
var acquireLeaseScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[4]), ARGV[3])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
redis.call('SET', KEYS[1], 1, 'PX', ARGV[2])
return 1
`)

// KEYS: lease, polls. ARGV: presence TTL ms, poll member.
var releaseLeaseScript = redis.NewScript(`
redis.call('ZREM', KEYS[2], ARGV[2])
redis.call('SET', KEYS[1], 1, 'PX', ARGV[1])
return 1
`)

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func (c *Coordinator) load(ctx context.Context, id roomID) (room, string, error) {
	values, err := c.valkey.HMGet(ctx, c.roomKey(id), "rev", "state").Result()
	if err != nil {
		return room{}, "", unavailable(err)
	}
	rev, okRev := values[0].(string)
	state, okState := values[1].(string)
	if !okRev || !okState {
		return room{}, "", ErrNotFound
	}
	var r room
	if err := json.Unmarshal([]byte(state), &r); err != nil {
		return room{}, "", fmt.Errorf("piperelay: decode room state: %w", err)
	}
	return r, rev, nil
}

// Ten receivers and a sender touch one room, so a few retries always settle.
const maxCASAttempts = 32

// A lost compare-and-set race re-reads and reruns fn; the answer is returned after the write.
func (c *Coordinator) update(ctx context.Context, id roomID, fn func(r *room, now time.Time) (bool, error)) (room, error) {
	for range maxCASAttempts {
		r, rev, err := c.load(ctx, id)
		if err != nil {
			return room{}, err
		}
		now := time.Now()
		write := c.settle(&r, now)
		changed, answer := fn(&r, now)
		if !write && !changed {
			return r, answer
		}
		ok, err := c.compareAndSet(ctx, id, rev, r, now)
		if err != nil {
			return room{}, err
		}
		if ok {
			return r, answer
		}
	}
	return room{}, fmt.Errorf("%w: room update kept losing the compare-and-set race", ErrUnavailable)
}

func (c *Coordinator) view(ctx context.Context, id roomID) (room, error) {
	return c.update(ctx, id, func(*room, time.Time) (bool, error) { return false, nil })
}

// Same rules as the cleaner, so readers see the end at once. Before streaming: the 3-minute window
// (ErrExpired) or a chosen connection idle past the presence timeout (ErrSessionClosed); streaming: MaxTransferDuration.
func (c *Coordinator) settle(r *room, now time.Time) bool {
	switch {
	case r.ended():
		return false
	case r.streaming():
		if now.After(r.StreamStartedAt.Add(c.maxTransferDuration)) {
			r.end(causeExpired)
			return true
		}
		return false
	case now.After(r.ExpiresAt):
		r.end(causeExpired)
		return true
	case c.chosenAbandoned(r, now):
		r.end(causeClosed)
		return true
	}
	return false
}

func (c *Coordinator) chosenAbandoned(r *room, now time.Time) bool {
	sel := r.conn(r.Selected)
	return sel != nil && !sel.Attached && now.After(r.SelectedAt.Add(c.presenceTimeout))
}

func (c *Coordinator) compareAndSet(ctx context.Context, id roomID, rev string, r room, now time.Time) (bool, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return false, fmt.Errorf("piperelay: encode room state: %w", err)
	}
	var ttl time.Duration
	score := ""
	switch {
	case r.ended():
		ttl, score = endedRetention, "-"
	case r.streaming():
		deadline := r.StreamStartedAt.Add(c.maxTransferDuration)
		ttl = deadline.Sub(now) + endedRetention
		score = strconv.FormatInt(deadline.UnixMilli(), 10)
	default:
		ttl = r.ExpiresAt.Sub(now) + endedRetention
	}
	ttl = max(ttl, time.Second)
	n, err := casRoomScript.Run(ctx, c.valkey,
		[]string{c.roomKey(id), c.roomsKey(), c.eventChannel(id)},
		rev, data, ttl.Milliseconds(), string(id), score,
	).Int()
	if err != nil {
		return false, unavailable(err)
	}
	return n == 1, nil
}

// Runs on a short context detached from the caller's, which is often already cancelled.
func (c *Coordinator) endRoom(ctx context.Context, id roomID, cause string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endRoomTimeout)
	defer cancel()
	_, _ = c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		if r.ended() {
			return false, nil
		}
		r.end(cause)
		return true, nil
	})
}

// Dropping is not a decision: nothing is blocked and nothing ends.
func (c *Coordinator) pruneIdle(ctx context.Context, id roomID, r room) (room, error) {
	if r.ended() || r.Selected != "" {
		return r, nil
	}
	pending := r.pending()
	if len(pending) == 0 {
		return r, nil
	}
	pipe := c.valkey.Pipeline()
	checks := make([]*redis.IntCmd, len(pending))
	for i, conn := range pending {
		checks[i] = pipe.Exists(ctx, c.leaseKey(id, conn.ID))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return room{}, unavailable(err)
	}
	idle := map[string]bool{}
	for i, conn := range pending {
		if checks[i].Val() == 0 && !conn.Attached {
			idle[conn.ID] = true
		}
	}
	if len(idle) == 0 {
		return r, nil
	}
	return c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		kept := r.Connections[:0:0]
		for _, conn := range r.Connections {
			if idle[conn.ID] && conn.Result == "" && !conn.Attached {
				continue
			}
			kept = append(kept, conn)
		}
		if len(kept) == len(r.Connections) {
			return false, nil
		}
		r.Connections = kept
		r.Version++
		return true, nil
	})
}

// A waiter registers before it reads the room, so a change between read and wait still wakes it.
type roomHub struct {
	mu      sync.Mutex
	waiters map[roomID]map[chan struct{}]struct{}
}

func (h *roomHub) watch(id roomID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.waiters == nil {
		h.waiters = map[roomID]map[chan struct{}]struct{}{}
	}
	if h.waiters[id] == nil {
		h.waiters[id] = map[chan struct{}]struct{}{}
	}
	h.waiters[id][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.waiters[id], ch)
		if len(h.waiters[id]) == 0 {
			delete(h.waiters, id)
		}
		h.mu.Unlock()
	}
}

func (h *roomHub) fire(id roomID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.waiters[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
