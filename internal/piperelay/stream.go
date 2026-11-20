package piperelay

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Pod-to-pod stream hop: ciphertext goes request body -> io.Pipe -> receiver response, never through Valkey.
const (
	// Followed by the room ID (an HMAC, never the 6-digit code).
	StreamPathPrefix    = "/internal/pipe/stream/"
	InternalTokenHeader = "X-Flick-Internal-Token"
	// One-use secret; Valkey holds only its SHA-256.
	StreamSecretHeader = "X-Flick-Pipe-Stream-Secret"
	kindHeader         = "Flick-Kind"
	filenameHeader     = "Flick-Encrypted-Filename"
	// Matches the public /send and /receive deadline (internal/httpapi/piperelay.go).
	streamHopDeadline = 15 * time.Minute
	streamDialTimeout = 5 * time.Second
)

type streamSlot struct {
	// Unbuffered: a pipe is handed over only while Receive is still waiting.
	handoff chan streamHandoff
	claimed atomic.Bool
	// Closes when Receive stops waiting or its caller finished reading.
	done     chan struct{}
	doneOnce sync.Once
}

type streamHandoff struct {
	reader *io.PipeReader
	meta   SessionMetadata
}

func (s *streamSlot) finish() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (c *Coordinator) openSlot(id roomID) (*streamSlot, bool) {
	c.slotsMu.Lock()
	defer c.slotsMu.Unlock()
	if _, taken := c.slots[id]; taken {
		return nil, false
	}
	slot := &streamSlot{handoff: make(chan streamHandoff), done: make(chan struct{})}
	c.slots[id] = slot
	return slot, true
}

func (c *Coordinator) closeSlot(id roomID, slot *streamSlot) {
	c.slotsMu.Lock()
	if c.slots[id] == slot {
		delete(c.slots, id)
	}
	c.slotsMu.Unlock()
	slot.finish()
}

func (c *Coordinator) slot(id roomID) *streamSlot {
	c.slotsMu.Lock()
	defer c.slotsMu.Unlock()
	return c.slots[id]
}

// The chosen receiver leaving before the stream arrives ends the session: nobody else can take over.
// The caller must call the returned cleanup once it stopped reading.
func (c *Coordinator) Receive(ctx context.Context, rawCode, connID string) (*io.PipeReader, SessionMetadata, func(), error) {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return nil, SessionMetadata{}, nil, err
	}
	changed, unwatch := c.hub.watch(id)
	defer unwatch()

	r, err := c.view(ctx, id)
	if err != nil {
		return nil, SessionMetadata{}, nil, err
	}
	if err := receiveAnswer(&r, connID); err != nil {
		return nil, SessionMetadata{}, nil, err
	}
	// The slot exists before the room advertises this replica, so the stream never arrives before Receive is ready.
	slot, opened := c.openSlot(id)
	if !opened {
		return nil, SessionMetadata{}, nil, ErrAlreadyConnected
	}
	r, err = c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		if err := receiveAnswer(r, connID); err != nil {
			return false, err
		}
		conn := r.conn(connID)
		conn.Attached = true
		r.ReceiverIP = c.advertiseIP.String()
		r.Version++
		return true, nil
	})
	if err != nil {
		c.closeSlot(id, slot)
		return nil, SessionMetadata{}, nil, err
	}

	cleanup := func() {
		c.closeSlot(id, slot)
		c.endRoom(ctx, id, causeClosed)
	}
	leave := func(cause error) (*io.PipeReader, SessionMetadata, func(), error) {
		cleanup()
		return nil, SessionMetadata{}, nil, cause
	}

	resync := time.NewTicker(resyncInterval)
	defer resync.Stop()
	expiry := time.NewTimer(time.Until(r.ExpiresAt))
	defer expiry.Stop()
	for {
		select {
		case h := <-slot.handoff:
			return h.reader, h.meta, cleanup, nil
		case <-ctx.Done():
			return leave(ctx.Err())
		case <-c.closed:
			return leave(ErrSessionClosed)
		case <-changed:
		case <-resync.C:
		case <-expiry.C:
		}
		current, err := c.view(ctx, id)
		if errors.Is(err, ErrUnavailable) && time.Now().Before(r.ExpiresAt) {
			// One failed read retries at the next resync instead of ending a transfer still on track.
			continue
		}
		if err != nil {
			return leave(err)
		}
		if current.ended() {
			c.closeSlot(id, slot)
			return nil, SessionMetadata{}, nil, current.cause()
		}
	}
}

func receiveAnswer(r *room, connID string) error {
	conn := r.conn(connID)
	if conn != nil && conn.loserResult() != nil {
		return conn.loserResult()
	}
	switch {
	case r.ended():
		return r.cause()
	case conn == nil:
		return ErrNotFound
	case conn.Attached:
		return ErrAlreadyConnected
	case r.Selected != connID:
		return ErrAwaitingDecision
	}
	return nil
}

func (c *Coordinator) Send(ctx context.Context, rawCode, senderToken string, meta SessionMetadata, body io.Reader) (int64, error) {
	id, err := c.roomIDFor(rawCode)
	if err != nil {
		return 0, err
	}
	claimed, err := c.update(ctx, id, func(r *room, _ time.Time) (bool, error) {
		if !r.senderMatches(senderToken) {
			return false, ErrNotFound
		}
		if r.ended() {
			return false, r.cause()
		}
		if r.SenderClaimed {
			return false, ErrAlreadyConnected
		}
		r.SenderClaimed = true
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	// After the claim every later Send answers ErrAlreadyConnected, so any failure must end the session.
	abandon := func(err error) (int64, error) {
		c.endRoom(ctx, id, causeClosed)
		return 0, err
	}

	if err := c.waitForReceiver(ctx, id, claimed.ExpiresAt); err != nil {
		return abandon(err)
	}

	// Cancel and the lazy end rules write the same room with compare-and-set, so a session either ends
	// before its stream starts or streams before anything can end it.
	secret, err := randomToken(tokenLengthBytes)
	if err != nil {
		return abandon(err)
	}
	r, err := c.update(ctx, id, func(r *room, now time.Time) (bool, error) {
		if r.ended() {
			return false, r.cause()
		}
		r.StreamStartedAt = now
		r.StreamSecretHash = sha256Hex(secret)
		return true, nil
	})
	if err != nil {
		return abandon(err)
	}
	return c.stream(ctx, id, r, secret, meta, body)
}

// Re-reads on every change and at the next deadline so the lazy end rules apply without the cleaner.
// Send ends the session on every error this returns.
func (c *Coordinator) waitForReceiver(ctx context.Context, id roomID, expiresAt time.Time) error {
	changed, unwatch := c.hub.watch(id)
	defer unwatch()
	for {
		next := resyncInterval
		r, err := c.view(ctx, id)
		switch {
		case errors.Is(err, ErrUnavailable) && time.Now().Before(expiresAt):
		case err != nil:
			return err
		case r.ended():
			return r.cause()
		case r.receiverReady():
			return nil
		default:
			next = min(time.Until(r.ExpiresAt), resyncInterval)
			if r.Selected != "" {
				next = min(next, time.Until(r.SelectedAt.Add(c.presenceTimeout)))
			}
		}
		timer := time.NewTimer(max(next, time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Atomic: the HTTP transport may still be reading after Do returns.
type countingReader struct {
	src     io.Reader
	n       atomic.Int64
	readErr atomic.Pointer[error]
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	r.n.Add(int64(n))
	if err != nil && !errors.Is(err, io.EOF) {
		r.readErr.CompareAndSwap(nil, &err)
	}
	return n, err
}

func (r *countingReader) err() error {
	if p := r.readErr.Load(); p != nil {
		return *p
	}
	return nil
}

// The target is only an IP literal from the room and the fixed internal port, so a tampered room
// cannot redirect it. io.ErrClosedPipe means the receiver side failed.
func (c *Coordinator) stream(ctx context.Context, id roomID, r room, secret string, meta SessionMetadata, body io.Reader) (int64, error) {
	ip, err := netip.ParseAddr(r.ReceiverIP)
	if err != nil {
		c.endRoom(ctx, id, causeClosed)
		return 0, io.ErrClosedPipe
	}
	target := netip.AddrPortFrom(ip.Unmap(), c.internalPort)

	streamCtx, cancel := context.WithDeadline(ctx, r.StreamStartedAt.Add(c.maxTransferDuration))
	defer cancel()
	counted := &countingReader{src: body}
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, "http://"+target.String()+StreamPathPrefix+string(id), counted)
	if err != nil {
		c.endRoom(ctx, id, causeClosed)
		return 0, err
	}
	req.Header.Set(InternalTokenHeader, c.internalToken)
	req.Header.Set(StreamSecretHeader, secret)
	if meta.Kind != "" {
		req.Header.Set(kindHeader, meta.Kind)
	}
	if meta.EncryptedFilename != "" {
		req.Header.Set(filenameHeader, meta.EncryptedFilename)
	}

	resp, err := c.streamClient.Do(req)
	delivered := err == nil && resp.StatusCode == http.StatusOK
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}
	n := counted.n.Load()
	if delivered {
		// Covers a receiver replica that could not reach Valkey.
		c.endRoom(ctx, id, causeClosed)
		return n, nil
	}

	c.endRoom(ctx, id, causeClosed)
	switch {
	case counted.err() != nil:
		return n, counted.err()
	case ctx.Err() != nil:
		return n, ctx.Err()
	case streamCtx.Err() != nil:
		return n, ErrExpired
	default:
		return n, io.ErrClosedPipe
	}
}

// Responds 200 only after the whole body reached the receiver, so the sender can tell completion from a receiver that left.
func (c *Coordinator) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+StreamPathPrefix+"{room}", c.serveStream)
	return mux
}

func (c *Coordinator) serveStream(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get(InternalTokenHeader)
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(c.internalToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw := r.PathValue("room")
	secret := r.Header.Get(StreamSecretHeader)
	if !validRoomID(raw) || secret == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(streamHopDeadline))
	_ = rc.SetWriteDeadline(time.Now().Add(streamHopDeadline))

	err := c.acceptStream(r.Context(), roomID(raw), secret, SessionMetadata{
		Kind:              r.Header.Get(kindHeader),
		EncryptedFilename: r.Header.Get(filenameHeader),
	}, r.Body)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, errReceiverGone):
		http.Error(w, "receiver not waiting", http.StatusNotFound)
	case errors.Is(err, errStreamForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, "stream failed", http.StatusBadGateway)
	}
}

var errStreamForbidden = errors.New("piperelay: stream secret does not match a streaming room")

// Returns nil only after the receiver's caller finished reading.
func (c *Coordinator) acceptStream(ctx context.Context, id roomID, secret string, meta SessionMetadata, body io.Reader) error {
	slot := c.slot(id)
	if slot == nil {
		return errReceiverGone
	}
	r, err := c.view(ctx, id)
	if err != nil {
		return err
	}
	given := sha256Hex(secret)
	if r.ended() || !r.streaming() || subtle.ConstantTimeCompare([]byte(given), []byte(r.StreamSecretHash)) != 1 {
		return errStreamForbidden
	}
	if !slot.claimed.CompareAndSwap(false, true) {
		return errStreamForbidden
	}

	reader, writer := io.Pipe()
	select {
	case slot.handoff <- streamHandoff{reader: reader, meta: meta}:
	case <-slot.done:
		return errReceiverGone
	case <-ctx.Done():
		return ctx.Err()
	}

	if _, err := copyWithPool(writer, body); err != nil {
		_ = writer.CloseWithError(err)
		return err
	}
	_ = writer.Close() // signals clean EOF to the receiver
	select {
	case <-slot.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newStreamTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: streamDialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		// Pod-to-pod traffic never goes through HTTP_PROXY.
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
}
