package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/requests"
	"github.com/go-chi/chi/v5"
)

// Request capabilities are accepted only in a single Authorization header.
func requestBearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(r.Header.Values("Authorization")) != 1 {
		return ""
	}
	return token
}

func (s Server) requestReady(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.requests == nil {
			writeRequestError(w, errors.New("request store unavailable"))
			return
		}
		if r.URL.RawQuery != "" {
			writeRequestError(w, requests.ErrInvalid)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s Server) requestRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One bucket per client across IDs and operations: rotating IDs must not
		// bypass the limit or allocate one bucket per guessed request ID.
		if !s.requestLimiter.allow(s.requestLimiter.clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, requests.ErrTooLarge
		}
		return nil, requests.ErrInvalid
	}
	return body, nil
}

func (s Server) createRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(w, r, 4096)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	var in struct {
		PublicKey  string `json:"public_key"`
		TTLSeconds *int   `json:"ttl_seconds"`
	}
	if err := requests.DecodeObject(body, &in, []string{"public_key"}, []string{"ttl_seconds"}); err != nil {
		writeRequestError(w, err)
		return
	}
	created, err := s.requests.Create(r.Context(), in.PublicKey, in.TTLSeconds)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s Server) getRequestInstructions(w http.ResponseWriter, r *http.Request) {
	result, err := s.requests.Instructions(r.Context(), chi.URLParam(r, "id"), requestBearer(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s Server) submitRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(w, r, s.createSecretBodyLimit())
	if err != nil {
		writeRequestError(w, err)
		return
	}
	var in requests.SubmitInput
	if err := requests.DecodeObject(body, &in, []string{"generation", "attempt_token", "kind", "size_bytes", "envelope", "ciphertext"}, nil); err != nil {
		writeRequestError(w, err)
		return
	}
	result, err := s.requests.Submit(r.Context(), chi.URLParam(r, "id"), requestBearer(r), in)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s Server) getRequestAttempt(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(w, r, 1024)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	var in struct {
		Generation   int    `json:"generation"`
		AttemptToken string `json:"attempt_token"`
	}
	if err := requests.DecodeObject(body, &in, []string{"generation", "attempt_token"}, nil); err != nil {
		writeRequestError(w, err)
		return
	}
	result, err := s.requests.Attempt(r.Context(), chi.URLParam(r, "id"), requestBearer(r), in.Generation, in.AttemptToken)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s Server) getRequestOwner(w http.ResponseWriter, r *http.Request) {
	result, err := s.requests.Owner(r.Context(), chi.URLParam(r, "id"), requestBearer(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s Server) openRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(w, r, 4096)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	if len(body) != 0 {
		writeRequestError(w, requests.ErrInvalid)
		return
	}
	result, err := s.requests.Open(r.Context(), chi.URLParam(r, "id"), requestBearer(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s Server) revokeRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readRequestBody(w, r, 4096)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	if len(body) != 0 {
		writeRequestError(w, requests.ErrInvalid)
		return
	}
	if err := s.requests.Revoke(r.Context(), chi.URLParam(r, "id"), requestBearer(r)); err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "cancelled"})
}

func writeRequestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, requests.ErrUnavailable):
		writeError(w, http.StatusNotFound, "request_unavailable", "request is unavailable")
	case errors.Is(err, requests.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "input does not match the request contract")
	case errors.Is(err, requests.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request payload exceeds the inline limit")
	case errors.Is(err, requests.ErrConflict):
		writeError(w, http.StatusConflict, "request_conflict", "request state or attempt conflicts")
	default:
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "request storage is temporarily unavailable")
	}
}
