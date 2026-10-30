package httpapi

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/piperelay"
)

type pipeInitResponse struct {
	Code      string `json:"code"`
	Salt      string `json:"salt"`
	ExpiresIn int    `json:"expires_in"`
}

type pipeStatusResponse struct {
	Status string `json:"status"`
}

type pipeSendResponse struct {
	Status           string `json:"status"`
	BytesTransferred int64  `json:"bytes_transferred"`
}

func (s Server) initPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeRelayEnabled || s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return
	}

	code, salt, expiresIn, err := s.pipeCoordinator.Init(r.Context())
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
		Code:      code,
		Salt:      base64.StdEncoding.EncodeToString(salt),
		ExpiresIn: expiresIn,
	})
}

func (s Server) waitPipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeRelayEnabled || s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return
	}

	code := r.Header.Get("X-Flick-Pipe-Code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Flick-Pipe-Code header")
		return
	}

	// Extend read deadline for waiting on receiver (up to 3.5 minutes)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(4 * time.Minute))

	err := s.pipeCoordinator.WaitReceiver(r.Context(), code)
	if err != nil {
		switch {
		case errors.Is(err, piperelay.ErrInvalidCode):
			writeError(w, http.StatusBadRequest, "bad_request", "invalid 6-digit pipe code")
		case errors.Is(err, piperelay.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "pipe rendezvous not found or expired")
		case errors.Is(err, piperelay.ErrExpired):
			writeError(w, http.StatusGone, "expired", "pipe rendezvous expired")
		case errors.Is(err, piperelay.ErrSessionBurned):
			writeError(w, http.StatusGone, "burned", "pipe session burned")
		case errors.Is(err, piperelay.ErrSessionClosed):
			writeError(w, http.StatusGone, "closed", "pipe session closed")
		default:
			if r.Context().Err() != nil {
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, pipeStatusResponse{Status: "ready"})
}

func (s Server) receivePipe(w http.ResponseWriter, r *http.Request) {
	if !s.pipeRelayEnabled || s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return
	}

	code := r.Header.Get("X-Flick-Pipe-Code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Flick-Pipe-Code header")
		return
	}

	// Extend write deadline to accommodate streaming large payloads (up to 15m)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))

	pr, salt, meta, cleanup, err := s.pipeCoordinator.Receive(r.Context(), code)
	if err != nil {
		// Record failed attempt against this code for burn calculation
		burned, _ := s.pipeCoordinator.RecordFailedAttempt(code)
		if burned || errors.Is(err, piperelay.ErrSessionBurned) {
			writeError(w, http.StatusGone, "burned", "pipe rendezvous burned due to excessive failed attempts")
			return
		}

		switch {
		case errors.Is(err, piperelay.ErrInvalidCode):
			writeError(w, http.StatusBadRequest, "bad_request", "invalid 6-digit pipe code")
		case errors.Is(err, piperelay.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "pipe rendezvous not found or expired")
		case errors.Is(err, piperelay.ErrExpired):
			writeError(w, http.StatusGone, "expired", "pipe rendezvous expired")
		case errors.Is(err, piperelay.ErrAlreadyConnected):
			writeError(w, http.StatusConflict, "already_connected", "receiver is already connected to this pipe session")
		case errors.Is(err, piperelay.ErrSessionClosed):
			writeError(w, http.StatusGone, "closed", "pipe session closed")
		default:
			if r.Context().Err() != nil {
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
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
	if !s.pipeRelayEnabled || s.pipeCoordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "live pipe relay is disabled")
		return
	}

	code := r.Header.Get("X-Flick-Pipe-Code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing X-Flick-Pipe-Code header")
		return
	}

	kind := r.Header.Get("Flick-Kind")
	encryptedFilename := r.Header.Get("Flick-Encrypted-Filename")

	// Extend read deadline to accommodate streaming large uploads (up to 15m)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(15 * time.Minute))

	n, err := s.pipeCoordinator.Send(r.Context(), code, piperelay.SessionMetadata{
		Kind:              kind,
		EncryptedFilename: encryptedFilename,
	}, r.Body)

	if err != nil {
		switch {
		case errors.Is(err, piperelay.ErrInvalidCode):
			writeError(w, http.StatusBadRequest, "bad_request", "invalid 6-digit pipe code")
		case errors.Is(err, piperelay.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "pipe rendezvous not found or expired")
		case errors.Is(err, piperelay.ErrExpired):
			writeError(w, http.StatusGone, "expired", "pipe rendezvous expired")
		case errors.Is(err, piperelay.ErrAlreadyConnected):
			writeError(w, http.StatusConflict, "already_connected", "sender is already active on this pipe session")
		case errors.Is(err, piperelay.ErrSessionBurned):
			writeError(w, http.StatusGone, "burned", "pipe session burned")
		case errors.Is(err, piperelay.ErrSessionClosed):
			writeError(w, http.StatusGone, "closed", "pipe session closed")
		case errors.Is(err, io.ErrClosedPipe):
			writeError(w, http.StatusBadGateway, "receiver_disconnected", "receiver disconnected during live stream")
		default:
			if r.Context().Err() != nil {
				return
			}
			writeError(w, http.StatusBadGateway, "transfer_failed", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, pipeSendResponse{
		Status:           "completed",
		BytesTransferred: n,
	})
}
