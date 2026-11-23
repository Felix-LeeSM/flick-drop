package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
)

const (
	pipeCodeHeader       = "X-Flick-Pipe-Code"
	pipeSenderHeader     = "X-Flick-Pipe-Sender"
	pipeConnectionHeader = "X-Flick-Pipe-Connection"
	// Under Cloudflare's 100 s idle cut.
	pipeWaitMaxHold = 25 * time.Second
	// A receiver whose network died stays listed for up to this hold plus the coordinator's presence timeout.
	pipeStatusMaxHold = 10 * time.Second
	// ~6 polls/min per waiting receiver x MaxPendingPerNetwork(3), with room for several sessions behind one NAT.
	pipeStatusRatePerMinute = 120
	pipeInitBodyMax         = 1024
	// A hybrid ML-KEM-768 + X25519 recipient (web/src/lib/crypto/age.ts) is 1,959 characters.
	pipeRecipientMaxChars = 4096
	pipeJoinBodyMax       = pipeRecipientMaxChars + 1024
)

var pipeRecipientPattern = regexp.MustCompile(`^age1pq1[02-9ac-hj-np-z]+$`)

// Same rule as internal/requests/validation.go; canonical re-encoding keeps one spelling per key.
func validPipeRecipient(raw string) bool {
	if len(raw) > pipeRecipientMaxChars || !pipeRecipientPattern.MatchString(raw) {
		return false
	}
	recipient, err := age.ParseHybridRecipient(raw)
	return err == nil && recipient.String() == raw
}

type pipeJoinRequest struct {
	// The API relays only ciphertext it cannot open.
	Recipient string `json:"recipient"`
}

type pipeInitRequest struct{}

type pipeInitResponse struct {
	Code        string `json:"code"`
	ExpiresIn   int    `json:"expires_in"`
	SenderToken string `json:"sender_token"`
}

type pipeJoinResponse struct {
	ConnectionID string `json:"connection_id"`
	VerifyCode   string `json:"verify_code"`
	Device       string `json:"device"`
	ExpiresIn    int    `json:"expires_in"`
}

type pipePendingConnection struct {
	ConnectionID     string `json:"connection_id"`
	Index            int    `json:"index"`
	VerifyCode       string `json:"verify_code"`
	Device           string `json:"device"`
	Recipient        string `json:"recipient"`
	ConnectedSeconds int    `json:"connected_seconds"`
}

// Sender only.
type pipeWaitResponse struct {
	Status      string                  `json:"status"`
	Version     uint64                  `json:"version"`
	Recipient   string                  `json:"recipient,omitempty"`
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
	if s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return false
	}
	return true
}

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

// Messages are fixed strings: codes, verification numbers, tokens, and User-Agents never appear.
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
	case errors.Is(err, piperelay.ErrPollInProgress):
		writeError(w, http.StatusConflict, "poll_in_progress", "this device already has a status poll open")
	case errors.Is(err, piperelay.ErrAwaitingDecision):
		writeError(w, http.StatusConflict, "awaiting_decision", "the sender has not picked this device yet")
	case errors.Is(err, piperelay.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", "connection is no longer waiting")
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
	case errors.Is(err, piperelay.ErrUnavailable):
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is temporarily unavailable")
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

	var body pipeInitRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, pipeInitBodyMax))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid pipe init body")
		return
	}

	result, err := s.pipeCoordinator.Init(r.Context())
	if err != nil {
		if errors.Is(err, piperelay.ErrCapacityExceeded) {
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusTooManyRequests, "capacity_exceeded", "server is at maximum concurrent pipe relay capacity")
			return
		}
		if errors.Is(err, piperelay.ErrUnavailable) {
			writePipeError(w, r, err)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to initialize pipe rendezvous")
		return
	}

	writeJSON(w, http.StatusCreated, pipeInitResponse{
		Code:        result.Code,
		ExpiresIn:   result.ExpiresIn,
		SenderToken: result.SenderToken,
	})
}

// Only hashes reach the coordinator. The network is the trusted-proxy client IP with IPv6 collapsed to /64.
// Key = SHA-256(network, User-Agent), deliberately coarse; Network = SHA-256(network).
func (s Server) pipeJoinClient(r *http.Request, recipient string) piperelay.JoinClient {
	network := s.pipeJoinLimiter.clientNetwork(r)
	return piperelay.JoinClient{
		Device:    pipeDeviceLabel(r.UserAgent()),
		Recipient: recipient,
		Key:       sha256.Sum256([]byte(network + "\n" + r.UserAgent())),
		Network:   sha256.Sum256([]byte("network\n" + network)),
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
	var body pipeJoinRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, pipeJoinBodyMax))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_recipient", "join body must be JSON with an age recipient")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_recipient", "join body must contain exactly one JSON object")
		return
	}
	if !validPipeRecipient(body.Recipient) {
		writeError(w, http.StatusBadRequest, "invalid_recipient", "recipient must be a hybrid age recipient (age1pq1...)")
		return
	}

	result, err := s.pipeCoordinator.Join(r.Context(), headers[0], s.pipeJoinClient(r, body.Recipient))
	if err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pipeJoinResponse{
		ConnectionID: result.ConnectionID,
		VerifyCode:   result.VerifyCode,
		Device:       result.Device,
		ExpiresIn:    result.ExpiresIn,
	})
}

// Polling is what keeps the connection in the room.
func (s Server) statusPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeConnectionHeader)
	if !ok {
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(pipeStatusMaxHold + 30*time.Second))

	selected, err := s.pipeCoordinator.Status(r.Context(), headers[0], headers[1], pipeStatusMaxHold)
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
		response.Recipient = snapshot.Recipient
	}
	for _, pending := range snapshot.Pending {
		response.Connections = append(response.Connections, pipePendingConnection{
			ConnectionID:     pending.ID,
			Index:            pending.Index,
			VerifyCode:       pending.VerifyCode,
			Device:           pending.Device,
			Recipient:        pending.Recipient,
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
	if err := s.pipeCoordinator.Select(r.Context(), headers[0], headers[1], headers[2]); err != nil {
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
	if err := s.pipeCoordinator.Decline(r.Context(), headers[0], headers[1], headers[2]); err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "declined"})
}

func (s Server) leavePipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeConnectionHeader)
	if !ok {
		return
	}
	if err := s.pipeCoordinator.Leave(r.Context(), headers[0], headers[1]); err != nil {
		writePipeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "left"})
}

func (s Server) cancelPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeEnabled(w) {
		return
	}
	headers, ok := requirePipeHeaders(w, r, pipeCodeHeader, pipeSenderHeader)
	if !ok {
		return
	}
	if err := s.pipeCoordinator.Cancel(r.Context(), headers[0], headers[1]); err != nil {
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

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(15 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))

	pr, meta, cleanup, err := s.pipeCoordinator.Receive(r.Context(), headers[0], headers[1])
	if err != nil {
		writePipeError(w, r, err)
		return
	}
	defer cleanup()
	defer pr.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
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
			errors.Is(err, piperelay.ErrCancelled), errors.Is(err, piperelay.ErrUnavailable):
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
