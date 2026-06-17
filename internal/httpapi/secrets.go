package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Felix-LeeSM/burn-links/internal/secrets"
)

const createBodyOverheadLimit = int64(64 * 1024)

type createSecretRequest struct {
	Kind              string            `json:"kind"`
	Ciphertext        string            `json:"ciphertext"`
	Nonce             string            `json:"nonce"`
	KDF               secrets.KDFParams `json:"kdf"`
	EncryptedFilename *string           `json:"encrypted_filename"`
	ContentType       *string           `json:"content_type"`
	SizeBytes         int64             `json:"size_bytes"`
	TTLSeconds        int               `json:"ttl_seconds"`
	MaxViews          int               `json:"max_views"`
}

type createSecretResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
}

type getSecretResponse struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	Ciphertext        string            `json:"ciphertext"`
	Nonce             string            `json:"nonce"`
	KDF               secrets.KDFParams `json:"kdf"`
	EncryptedFilename *string           `json:"encrypted_filename,omitempty"`
	ContentType       *string           `json:"content_type,omitempty"`
	SizeBytes         int64             `json:"size_bytes"`
	ExpiresAt         string            `json:"expires_at"`
}

type consumeSecretResponse struct {
	ID       string `json:"id"`
	Consumed bool   `json:"consumed"`
}

func (s Server) createSecret(w http.ResponseWriter, r *http.Request) {
	bodyLimit := s.secretsPayloadLimit() + createBodyOverheadLimit
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
		return
	}
	defer r.Body.Close()

	if hasSensitiveField(body) {
		writeError(w, http.StatusBadRequest, "sensitive_field_forbidden", "passphrases, plaintext, and keys must not be sent to the API")
		return
	}

	var req createSecretRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body does not match the create secret contract")
		return
	}

	ciphertext, err := base64.StdEncoding.DecodeString(req.Ciphertext)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ciphertext", "ciphertext must be base64 encoded")
		return
	}

	created, err := s.secrets.Create(r.Context(), secrets.CreateInput{
		Kind:              req.Kind,
		Ciphertext:        ciphertext,
		Nonce:             req.Nonce,
		KDF:               req.KDF,
		EncryptedFilename: req.EncryptedFilename,
		ContentType:       req.ContentType,
		SizeBytes:         req.SizeBytes,
		TTLSeconds:        req.TTLSeconds,
		MaxViews:          normalizeMaxViews(req.MaxViews),
	})
	if err != nil {
		s.writeSecretError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, createSecretResponse{
		ID:        created.ID,
		ExpiresAt: created.ExpiresAt.Format(timeFormat),
	})
}

func (s Server) getSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	secret, err := s.secrets.Get(r.Context(), id)
	if err != nil {
		s.writeSecretError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, getSecretResponse{
		ID:                secret.ID,
		Kind:              secret.Kind,
		Ciphertext:        base64.StdEncoding.EncodeToString(secret.Ciphertext),
		Nonce:             secret.Nonce,
		KDF:               secret.KDF,
		EncryptedFilename: secret.EncryptedFilename,
		ContentType:       secret.ContentType,
		SizeBytes:         secret.SizeBytes,
		ExpiresAt:         secret.ExpiresAt.Format(timeFormat),
	})
}

func (s Server) consumeSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.secrets.Consume(r.Context(), id); err != nil {
		s.writeConsumeError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, consumeSecretResponse{
		ID:       id,
		Consumed: true,
	})
}

func (s Server) writeConsumeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrConsumed):
		writeError(w, http.StatusConflict, "consumed", "secret has already been consumed")
	default:
		s.writeSecretError(w, err)
	}
}

func (s Server) writeSecretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "secret was not found")
	case errors.Is(err, secrets.ErrConsumed):
		writeError(w, http.StatusGone, "consumed", "secret has already been consumed")
	case errors.Is(err, secrets.ErrExpired):
		writeError(w, http.StatusGone, "expired", "secret has expired")
	case errors.Is(err, secrets.ErrPayloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "encrypted payload is too large")
	case errors.Is(err, secrets.ErrUnsupportedKind):
		writeError(w, http.StatusBadRequest, "unsupported_kind", "only text secrets are supported in this milestone slice")
	case errors.Is(err, secrets.ErrUnsupportedViews):
		writeError(w, http.StatusBadRequest, "unsupported_max_views", "only one-time secrets are supported")
	case errors.Is(err, secrets.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_secret", "secret metadata is invalid")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "request failed")
	}
}

func hasSensitiveField(body []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}

	for key := range raw {
		switch key {
		case "passphrase", "password", "plaintext", "key", "derived_key", "secret":
			return true
		}
	}
	return false
}

func normalizeMaxViews(value int) int {
	if value == 0 {
		return 1
	}
	return value
}

func (s Server) secretsPayloadLimit() int64 {
	return s.payloadInlineMaxBytes
}
