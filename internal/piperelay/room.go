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

// roomID names one rendezvous in Valkey: the hex HMAC-SHA-256 of the 6-digit
// code under CoordinatorOptions.KeySalt. A Valkey key dump therefore never
// reveals a code (a bare SHA-256 of 900,000 possible codes would).
type roomID string

func validRoomID(raw string) bool {
	if len(raw) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// Connection results stored in roomConn.Result.
const (
	resultSelected    = "selected"
	resultDeclined    = "declined"
	resultNotSelected = "not_selected"
)

// End causes stored in room.EndCause.
const (
	causeCancelled = "cancelled"
	causeExpired   = "expired"
	causeClosed    = "closed"
)

// room is the whole state of one rendezvous, stored as JSON in the
// "state" field of the Valkey hash {Namespace}room:{roomID}. It never holds
// ciphertext, the plaintext code, the sender token, or the stream secret:
// only their hashes.
type room struct {
	SenderTokenHash string    `json:"sender_token_hash"`
	ExpiresAt       time.Time `json:"expires_at"`
	// Version is what the sender's Wait long-poll compares; it moves only on
	// changes the sender sees (joins, decisions, receiver ready, pruning).
	Version     uint64     `json:"version"`
	NextIndex   int        `json:"next_index"`
	Connections []roomConn `json:"connections"`
	// DeclinedClients holds the HMAC'd client keys the sender declined.
	DeclinedClients []string  `json:"declined_clients"`
	Selected        string    `json:"selected,omitempty"`
	SelectedAt      time.Time `json:"selected_at,omitzero"`
	SenderClaimed   bool      `json:"sender_claimed,omitempty"`
	// ReceiverIP is the IP literal of the pod holding the chosen receiver's
	// /receive; the sender's pod dials it on CoordinatorOptions.InternalPort.
	ReceiverIP       string    `json:"receiver_ip,omitempty"`
	StreamSecretHash string    `json:"stream_secret_hash,omitempty"`
	StreamStartedAt  time.Time `json:"stream_started_at,omitzero"`
	EndCause         string    `json:"end_cause,omitempty"`
}

// roomConn is one receiver that entered the right code.
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
	// Result is empty while the connection waits for the sender's pick.
	Result string `json:"result,omitempty"`
}

func (r *room) ended() bool     { return r.EndCause != "" }
func (r *room) streaming() bool { return !r.StreamStartedAt.IsZero() }

// end marks the room ended with cause; the first cause wins.
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

// pending lists undecided connections in join order (Connections is kept in
// join order).
func (r *room) pending() []*roomConn {
	out := make([]*roomConn, 0, len(r.Connections))
	for i := range r.Connections {
		if r.Connections[i].Result == "" {
			out = append(out, &r.Connections[i])
		}
	}
	return out
}

// receiverReady reports whether the chosen receiver has opened Receive.
func (r *room) receiverReady() bool {
	sel := r.conn(r.Selected)
	return sel != nil && sel.Attached
}

// loserResult is the answer a connection the sender already settled keeps,
// even after the room ends.
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

// leaseKey counts the open Status polls of one connection. Its TTL is the
// presence timeout, refreshed when a poll starts and ends, so the key exists
// exactly while the connection counts as present, and a pod that dies
// mid-poll frees the count within the presence timeout.
func (c *Coordinator) leaseKey(id roomID, connID string) string {
	return c.namespace + "poll:" + string(id) + ":" + sha256Hex(connID)[:32]
}

// createRoomScript adds a room unless the concurrent-room cap is reached
// (-1) or the code is taken, including by an ended room's tombstone (0).
//
// KEYS: room, rooms. ARGV: roomID, now ms, max rooms, state, key TTL ms,
// rooms score (expiry ms).
var createRoomScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[2])
if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[3]) then return -1 end
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'rev', 1, 'state', ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
redis.call('ZADD', KEYS[2], ARGV[6], ARGV[1])
return 1
`)

// casRoomScript replaces the room state only if its rev is unchanged, then
// announces the change on the room's Pub/Sub channel in the same atomic step.
// A score of "-" removes the room from the live set; "" leaves the score.
//
// KEYS: room, rooms, event channel. ARGV: expected rev, state, key TTL ms,
// roomID, rooms score.
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

// acquireLeaseScript opens one Status poll: 0 when the connection already has
// ARGV[1] polls open. KEYS: lease. ARGV: max polls, presence TTL ms.
var acquireLeaseScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n > tonumber(ARGV[1]) then
  redis.call('DECR', KEYS[1])
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

// releaseLeaseScript closes one Status poll and restarts the presence TTL.
// KEYS: lease. ARGV: presence TTL ms.
var releaseLeaseScript = redis.NewScript(`
local n = redis.call('DECR', KEYS[1])
if n < 0 then redis.call('SET', KEYS[1], 0) end
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return 1
`)

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// load reads the room and its rev. A missing key is ErrNotFound.
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

// maxCASAttempts bounds the optimistic retries of one update. Ten waiting
// receivers and a sender touch one room, so a few retries always settle.
const maxCASAttempts = 32

// update reads the room, applies the rules that end it lazily (settle), runs
// fn, and writes the result back with a compare-and-set when settle or fn
// changed it. fn returns whether it changed the room and the caller's answer;
// the answer is returned after the write. A lost race re-reads and runs fn
// again on the fresh state.
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

// view reads the room with its lazy end rules applied.
func (c *Coordinator) view(ctx context.Context, id roomID) (room, error) {
	return c.update(ctx, id, func(*room, time.Time) (bool, error) { return false, nil })
}

// settle ends a room whose time ran out, the same rules the in-process
// cleaner applies, so every reader sees the end at once instead of up to one
// cleaner interval later:
//   - before streaming, the 3-minute rendezvous window closing ends it
//     (ErrExpired), and a chosen connection that has not opened Receive
//     within the presence timeout ends it (ErrSessionClosed);
//   - a streaming room is exempt from the rendezvous window and ends only
//     past MaxTransferDuration (ErrExpired).
//
// It reports whether it changed the room.
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

// compareAndSet writes r if the stored rev is still rev. The key outlives the
// state's own deadline by endedRetention, and an ended room stays as a
// tombstone for endedRetention so a caller already in the session learns how
// it ended.
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

// endRoom ends the room with cause unless it already ended. It runs on a
// short context detached from the caller's (often already cancelled) one.
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

// pruneIdle drops every waiting connection whose poll lease expired, so a
// joiner that never polls (or stopped) frees its slot. Dropping is not a
// decision: nothing is blocked and nothing ends. It returns the room as it
// is after pruning.
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

// roomHub fans the one Pub/Sub subscription of this process out to the local
// callers waiting on a room. A waiter registers before it reads the room, so
// a change between its read and its wait still wakes it.
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
