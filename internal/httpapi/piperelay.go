package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
)

const (
	pipeCodeHeader       = "X-Flick-Pipe-Code"
	pipeSenderHeader     = "X-Flick-Pipe-Sender"
	pipeConnectionHeader = "X-Flick-Pipe-Connection"
	// pipeWaitMaxHold bounds one /wait or /status long-poll. The caller
	// re-polls, so this only has to stay under proxy idle timeouts (Cloudflare
	// cuts idle responses at 100 seconds) and under the coordinator's 35-second
	// presence timeout (internal/piperelay/coordinator.go defaultPresenceTimeout).
	pipeWaitMaxHold = 25 * time.Second
	pipeInitBodyMax = 1024
)

type pipeInitRequest struct {
	RequireConfirmation bool `json:"require_confirmation"`
}

type pipeInitResponse struct {
	Code                string `json:"code"`
	Salt                string `json:"salt"`
	ExpiresIn           int    `json:"expires_in"`
	SenderToken         string `json:"sender_token"`
	RequireConfirmation bool   `json:"require_confirmation"`
}

type pipeJoinResponse struct {
	ConnectionID        string `json:"connection_id"`
	RequireConfirmation bool   `json:"require_confirmation"`
	VerifyCode          string `json:"verify_code,omitempty"`
	Device              string `json:"device,omitempty"`
	ExpiresIn           int    `json:"expires_in"`
}

type pipePendingConnection struct {
	ConnectionID     string `json:"connection_id"`
	Index            int    `json:"index"`
	VerifyCode       string `json:"verify_code"`
	Device           string `json:"device"`
	ConnectedSeconds int    `json:"connected_seconds"`
}

type pipeWaitResponse struct {
	Status      string                  `json:"status"`
	Version     uint64                  `json:"version"`
	Connections []pipePendingConnection `json:"connections"`
}

type pipeStatusResponse struct {
	Status string `json:"status"`
}

type pipeSendResponse struct {
	Status           string `json:"status"`
	BytesTransferred int64  `json:"bytes_transferred"`
}

func (s Server) pipeEnabled(w http.ResponseWriter) bool {
	if !s.pipeRelayEnabled || s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return false
	}
	return true
}

// requirePipeHeaders reads the named headers, answering 400 when one is missing.
func requirePipeHeaders(w http.ResponseWriter, r *http.Request, names ...string) ([]string, bool) {
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = r.Header.Get(name)
		if values[i] == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "missing "+name+" header")
			return nil, false
		}
	}
	return values, true
}

// writePipeError maps coordinator errors to responses. Messages are fixed
// strings: codes, verification numbers, tokens, and User-Agents never appear.
func writePipeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, piperelay.ErrInvalidCode):
		writeError(w, http.StatusBadRequest, "bad_request", "invalid 6-digit pipe code")
	case errors.Is(err, piperelay.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "pipe rendezvous not found or expired")
	case errors.Is(err, piperelay.ErrDeclined):
		writeError(w, http.StatusForbidden, "declined", "the sender declined this device")
	case errors.Is(err, piperelay.ErrAlreadyConnected):
		writeError(w, http.StatusConflict, "already_connected", "another device is already receiving this transfer")
	case errors.Is(err, piperelay.ErrRoomFull):
		writeError(w, http.StatusConflict, "room_full", "too many devices are waiting on this code")
	case errors.Is(err, piperelay.ErrNetworkFull):
		writeError(w, http.StatusConflict, "network_full", "too many devices from this network are waiting on this code")
	case errors.Is(err, piperelay.ErrAwaitingDecision):
		writeError(w, http.StatusConflict, "awaiting_decision", "the sender has not picked this device yet")
	case errors.Is(err, piperelay.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", "connection is no longer waiting")
	case errors.Is(err, piperelay.ErrConfirmationDisabled):
		writeError(w, http.StatusConflict, "confirmation_disabled", "session does not require confirmation")
	case errors.Is(err, piperelay.ErrStreaming):
		writeError(w, http.StatusConflict, "streaming", "transfer is already streaming")
	case errors.Is(err, piperelay.ErrNotSelected):
		writeError(w, http.StatusGone, "not_selected", "the sender chose another device")
	case errors.Is(err, piperelay.ErrCancelled):
		writeError(w, http.StatusGone, "cancelled", "the sender cancelled this transfer")
	case errors.Is(err, piperelay.ErrExpired):
		writeError(w, http.StatusGone, "expired", "pipe rendezvous expired")
	case errors.Is(err, piperelay.ErrSessionClosed):
		writeError(w, http.StatusGone, "closed", "pipe session closed")
	default:
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "pipe relay request failed")
	}
}

func (s Server) initPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}

	// The body is optional: an empty body means confirmation off.
	var body pipeInitRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, pipeInitBodyMax))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid pipe init body")
		return
	}

	result, err := s.pipeCoordinator.Init(r.Context(), piperelay.InitOptions{
		RequireConfirmation: body.RequireConfirmation,
	})
	if err != nil {
		if errors.Is(err, piperelay.ErrCapacityExceeded) {
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusTooManyRequests, "capacity_exceeded", "server is at maximum concurrent pipe relay capacity")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to initialize pipe rendezvous")
		return
	}

	writeJSON(w, http.StatusCreated, pipeInitResponse{
		Code:                result.Code,
		Salt:                base64.StdEncoding.EncodeToString(result.Salt),
		ExpiresIn:           result.ExpiresIn,
		SenderToken:         result.SenderToken,
		RequireConfirmation: result.RequireConfirmation,
	})
}

// pipeJoinClient describes a receiving client to the coordinator by hashes
// only. Both hashes start from the client network: the client IP resolved
// through FLICK_TRUSTED_PROXIES the same way as the rate limiter, with IPv6
// collapsed to its /64 (rateLimiter.clientNetwork), so rotating addresses
// inside one IPv6 prefix escapes neither a decline nor the per-network cap.
//
//   - Key, for the per-session decline block, is SHA-256 of the network and
//     the User-Agent. It is deliberately coarse: another device behind the same
//     NAT or /64 with the same browser and OS string is blocked too for that
//     one short session, and changing network or User-Agent gets past it. The
//     sender can decline again.
//   - Network, for piperelay.MaxPendingPerNetwork, is SHA-256 of the network.
//
// Only the hashes are kept, in memory, for the session's lifetime.
func (s Server) pipeJoinClient(r *http.Request) piperelay.JoinClient {
	network := s.pipeJoinLimiter.clientNetwork(r)
	return piperelay.JoinClient{
		Device:  pipeDeviceLabel(r.UserAgent()),
		Key:     sha256.Sum256([]byte(network + "\n" + r.UserAgent())),
		Network: sha256.Sum256([]byte("network\n" + network)),
	}
}

func (s Server) joinPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader)
	if !ok {
		return
	}

	result, err := s.pipeCoordinator.Join(headers[0], s.pipeJoinClient(r))
	if err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pipeJoinResponse{
		ConnectionID:        result.ConnectionID,
		RequireConfirmation: result.RequireConfirmation,
		VerifyCode:          result.VerifyCode,
		Device:              result.Device,
		ExpiresIn:           result.ExpiresIn,
	})
}

// statusPipe is a waiting receiver's long-poll (confirmation on). It answers
// {"status":"selected"} once the sender picked this connection, and the
// receiver then opens /receive; {"status":"waiting"} after at most
// pipeWaitMaxHold; or the connection's end as an error (declined,
// not_selected, cancelled, expired, closed). Polling is what keeps the
// connection in the room.
func (s Server) statusPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeConnectionHeader)
	if !ok {
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(pipeWaitMaxHold + 30*time.Second))

	selected, err := s.pipeCoordinator.Status(r.Context(), headers[0], headers[1], pipeWaitMaxHold)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writePipeError(w, r, err)
		return
	}
	status := "waiting"
	if selected {
		status = "selected"
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: status})
}

func (s Server) waitPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader)
	if !ok {
		return
	}
	var since uint64
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "since must be a non-negative integer")
			return
		}
		since = parsed
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(pipeWaitMaxHold + 30*time.Second))

	snapshot, err := s.pipeCoordinator.Wait(r.Context(), headers[0], headers[1], since, pipeWaitMaxHold)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writePipeError(w, r, err)
		return
	}

	now := time.Now()
	response := pipeWaitResponse{
		Status:      "waiting",
		Version:     snapshot.Version,
		Connections: make([]pipePendingConnection, 0, len(snapshot.Pending)),
	}
	if snapshot.ReceiverReady {
		response.Status = "ready"
	}
	for _, pending := range snapshot.Pending {
		response.Connections = append(response.Connections, pipePendingConnection{
			ConnectionID:     pending.ID,
			Index:            pending.Index,
			VerifyCode:       pending.VerifyCode,
			Device:           pending.Device,
			ConnectedSeconds: max(0, int(now.Sub(pending.JoinedAt).Seconds())),
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func (s Server) selectPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader, pipeConnectionHeader)
	if !ok {
		return
	}
	if err := s.pipeCoordinator.Select(headers[0], headers[1], headers[2]); err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "selected"})
}

func (s Server) declinePipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader, pipeConnectionHeader)
	if !ok {
		return
	}
	if err := s.pipeCoordinator.Decline(headers[0], headers[1], headers[2]); err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "declined"})
}

func (s Server) cancelPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader)
	if !ok {
		return
	}
	if err := s.pipeCoordinator.Cancel(headers[0], headers[1]); err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "cancelled"})
}

func (s Server) receivePipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeConnectionHeader)
	if !ok {
		return
	}

	// Extend deadlines to accommodate streaming large payloads (up to 15m)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(15 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))

	pr, salt, meta, cleanup, err := s.pipeCoordinator.Receive(r.Context(), headers[0], headers[1])
	if err != nil {
		// A rejected, late, or not-yet-picked receiver never counts against
		// the session.
		writePipeError(w, r, err)
		return
	}
	defer cleanup()
	defer pr.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Flick-Pipe-Salt", base64.StdEncoding.EncodeToString(salt))
	if meta.Kind != "" {
		w.Header().Set("Flick-Kind", meta.Kind)
	}
	if meta.EncryptedFilename != "" {
		w.Header().Set("Flick-Encrypted-Filename", meta.EncryptedFilename)
	}
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	_, _ = io.Copy(w, pr)
}

func (s Server) sendPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader)
	if !ok {
		return
	}

	kind := r.Header.Get("Flick-Kind")
	encryptedFilename := r.Header.Get("Flick-Encrypted-Filename")

	// Extend deadlines to accommodate streaming large uploads and writing completion status (up to 15m)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(15 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))

	n, err := s.pipeCoordinator.Send(r.Context(), headers[0], headers[1], piperelay.SessionMetadata{
		Kind:              kind,
		EncryptedFilename: encryptedFilename,
	}, r.Body)
	if err != nil {
		switch {
		case errors.Is(err, piperelay.ErrAlreadyConnected):
			writeError(w, http.StatusConflict, "already_connected", "sender is already active on this pipe session")
		case errors.Is(err, io.ErrClosedPipe):
			writeError(w, http.StatusBadGateway, "receiver_disconnected", "receiver disconnected during live stream")
		case errors.Is(err, piperelay.ErrInvalidCode), errors.Is(err, piperelay.ErrNotFound),
			errors.Is(err, piperelay.ErrExpired), errors.Is(err, piperelay.ErrSessionClosed),
			errors.Is(err, piperelay.ErrCancelled):
			writePipeError(w, r, err)
		default:
			if r.Context().Err() != nil {
				return
			}
			writeError(w, http.StatusBadGateway, "transfer_failed", "live stream transfer failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, pipeSendResponse{
		Status:           "completed",
		BytesTransferred: n,
	})
}
