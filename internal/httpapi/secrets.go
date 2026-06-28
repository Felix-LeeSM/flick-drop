package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Felix-LeeSM/flick-drop/internal/events"
	"github.com/Felix-LeeSM/flick-drop/internal/secrets"
	"github.com/Felix-LeeSM/flick-drop/internal/telemetry"
)

const createBodyOverheadLimit = int64(64 * 1024)

type createSecretRequest struct {
	Kind              string            `json:"kind"`
	Ciphertext        string            `json:"ciphertext"`
	Nonce             string            `json:"nonce"`
	KDF               secrets.KDFParams `json:"kdf"`
	Access            accessRequest     `json:"access"`
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

// presignedPOSTResponse hands the client a presigned POST form so it uploads
// the ciphertext straight to the bucket; the server never sees the bytes.
type presignedPOSTResponse struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	ExpiresAt string            `json:"expires_at"`
	Fields    map[string]string `json:"fields"`
	FileField string            `json:"file_field"`
}

type createSecretLargeResponse struct {
	ID        string                `json:"id"`
	ExpiresAt string                `json:"expires_at"`
	Upload    presignedPOSTResponse `json:"upload"`
}

type accessRequest struct {
	KDF   secrets.KDFParams `json:"kdf"`
	Proof string            `json:"proof"`
}

type accessMetadataResponse struct {
	KDF secrets.KDFParams `json:"kdf"`
}

type getSecretMetadataResponse struct {
	ID        string                  `json:"id"`
	Kind      string                  `json:"kind"`
	Access    *accessMetadataResponse `json:"access,omitempty"`
	SizeBytes int64                   `json:"size_bytes"`
	ExpiresAt string                  `json:"expires_at"`
}

type openSecretRequest struct {
	AccessProof string `json:"access_proof"`
}

type openSecretResponse struct {
	ID                string             `json:"id"`
	Kind              string             `json:"kind"`
	Ciphertext        string             `json:"ciphertext"`
	Nonce             string             `json:"nonce"`
	KDF               *secrets.KDFParams `json:"kdf,omitempty"`
	EncryptedFilename *string            `json:"encrypted_filename,omitempty"`
	ContentType       *string            `json:"content_type,omitempty"`
	SizeBytes         int64              `json:"size_bytes"`
	ExpiresAt         string             `json:"expires_at"`
}

type cleanupSecretRequest struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

type cleanupSecretResponse struct {
	ID      string `json:"id"`
	Cleaned bool   `json:"cleaned"`
}

func (s Server) createSecret(w http.ResponseWriter, r *http.Request) {
	bodyLimit := s.createSecretBodyLimit()
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
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}

	// Large payloads omit ciphertext: the client uploads it straight to the
	// bucket via a presigned POST, then calls /finalize. Small payloads take
	// the inline path below.
	if req.Ciphertext == "" {
		s.createLargeSecret(w, r, req)
		return
	}

	ciphertext, err := base64.StdEncoding.DecodeString(req.Ciphertext)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ciphertext", "ciphertext must be base64 encoded")
		return
	}
	// Model A secrets carry an access proof; Model B secrets omit it and the
	// store leaves the proof/KDF columns NULL.
	var accessProofHash string
	if req.Access.Proof != "" {
		accessProofHash, err = hashAccessProof(req.Access.Proof)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_access_proof", "access proof must be base64 encoded")
			return
		}
	}

	created, err := s.secrets.Create(r.Context(), secrets.CreateInput{
		Kind:              req.Kind,
		Ciphertext:        ciphertext,
		Nonce:             req.Nonce,
		KDF:               req.KDF,
		AccessKDF:         req.Access.KDF,
		AccessProofHash:   accessProofHash,
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

// createLargeSecret stages a pending_upload secret and returns a presigned POST
// form so the client uploads the ciphertext directly to the bucket.
func (s Server) createLargeSecret(w http.ResponseWriter, r *http.Request, req createSecretRequest) {
	var accessProofHash string
	if req.Access.Proof != "" {
		hashed, err := hashAccessProof(req.Access.Proof)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_access_proof", "access proof must be base64 encoded")
			return
		}
		accessProofHash = hashed
	}

	res, err := s.secrets.CreateLarge(r.Context(), secrets.CreateLargeInput{
		Kind:              req.Kind,
		Nonce:             req.Nonce,
		KDF:               req.KDF,
		AccessKDF:         req.Access.KDF,
		AccessProofHash:   accessProofHash,
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

	writeJSON(w, http.StatusCreated, createSecretLargeResponse{
		ID:        res.ID,
		ExpiresAt: res.ExpiresAt.Format(timeFormat),
		Upload: presignedPOSTResponse{
			URL:       res.Upload.URL,
			Method:    res.Upload.Method,
			ExpiresAt: res.Upload.ExpiresAt.Format(timeFormat),
			Fields:    res.Upload.Fields,
			FileField: res.Upload.FileField,
		},
	})
}

// finalizeSecret confirms a pending_upload large secret after the client has
// uploaded the object. The server HEADs the bucket to verify the object exists
// and is within the ciphertext cap, then flips the row to active.
func (s Server) finalizeSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, createBodyOverheadLimit))
	_ = r.Body.Close()

	if err := s.secrets.Finalize(r.Context(), id); err != nil {
		s.writeSecretError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "finalized": true})
}

func (s Server) getSecretMetadata(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	metadata, err := s.secrets.Metadata(r.Context(), id)
	if err != nil {
		s.writeSecretError(w, err)
		return
	}

	resp := getSecretMetadataResponse{
		ID:        metadata.ID,
		Kind:      metadata.Kind,
		SizeBytes: metadata.SizeBytes,
		ExpiresAt: metadata.ExpiresAt.Format(timeFormat),
	}
	// Model A exposes access KDF so the browser can derive the proof; Model B
	// omits the access block so the browser opens with the fragment key instead.
	if metadata.AccessKDF.Algorithm != "" {
		resp.Access = &accessMetadataResponse{KDF: metadata.AccessKDF}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s Server) openSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, createBodyOverheadLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
		return
	}
	defer r.Body.Close()

	if hasSensitiveField(body) {
		writeError(w, http.StatusBadRequest, "sensitive_field_forbidden", "passphrases, plaintext, and keys must not be sent to the API")
		return
	}

	var req openSecretRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body does not match the open secret contract")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}

	// Model A opens require a proof; Model B opens omit it (the link is the
	// capability). The store decides per-secret whether to verify.
	var accessProofHash string
	if req.AccessProof != "" {
		accessProofHash, err = hashAccessProof(req.AccessProof)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_access_proof", "access proof must be base64 encoded")
			return
		}
	}

	secret, err := s.openAndEnqueueCleanup(r.Context(), id, accessProofHash)
	if err != nil {
		s.writeOpenError(w, err)
		return
	}

	resp := openSecretResponse{
		ID:                secret.ID,
		Kind:              secret.Kind,
		Ciphertext:        base64.StdEncoding.EncodeToString(secret.Ciphertext),
		Nonce:             secret.Nonce,
		EncryptedFilename: secret.EncryptedFilename,
		ContentType:       secret.ContentType,
		SizeBytes:         secret.SizeBytes,
		ExpiresAt:         secret.ExpiresAt.Format(timeFormat),
	}
	// Model A (passphrase) carries a KDF so the browser can re-derive the key.
	// Model B omits it — the random key travels in the URL fragment.
	if secret.KDF.Algorithm != "" {
		resp.KDF = &secret.KDF
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s Server) openAndEnqueueCleanup(ctx context.Context, id string, accessProofHash string) (secrets.Secret, error) {
	if s.outbox == nil {
		return secrets.Secret{}, fmt.Errorf("outbox store is required")
	}
	jobID, err := s.newJobID()
	if err != nil {
		return secrets.Secret{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return secrets.Secret{}, fmt.Errorf("begin open cleanup transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	secret, err := s.secrets.OpenTx(ctx, tx, id, accessProofHash)
	if err != nil {
		if errors.Is(err, secrets.ErrInvalidAccess) {
			if commitErr := tx.Commit(); commitErr != nil {
				return secrets.Secret{}, fmt.Errorf("commit failed access attempt: %w", commitErr)
			}
		}
		return secrets.Secret{}, err
	}
	if _, err := s.outbox.EnqueueTx(ctx, tx, events.JobEvent{
		JobID:       jobID,
		Kind:        events.KindDeleteSecret,
		SecretID:    id,
		Reason:      events.ReasonConsumed,
		RequestedAt: time.Now().UTC(),
	}); err != nil {
		return secrets.Secret{}, fmt.Errorf("enqueue consumed cleanup job: %w", err)
	}
	// M3: S3-backed secrets also need their bucket object deleted. Enqueued as a
	// separate job so DB cleanup and object deletion can fail and retry on
	// their own schedules.
	if secret.StorageBackend == secrets.StorageS3 {
		objectJobID, err := s.newJobID()
		if err != nil {
			return secrets.Secret{}, err
		}
		if _, err := s.outbox.EnqueueTx(ctx, tx, events.JobEvent{
			JobID:       objectJobID,
			Kind:        events.KindDeleteOCIObject,
			ObjectKey:   secret.StorageKey,
			Reason:      events.ReasonConsumed,
			RequestedAt: time.Now().UTC(),
		}); err != nil {
			return secrets.Secret{}, fmt.Errorf("enqueue object cleanup job: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return secrets.Secret{}, fmt.Errorf("commit open cleanup transaction: %w", err)
	}
	telemetry.SecretOpened.Inc()
	return secret, nil
}

func (s Server) cleanupSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, createBodyOverheadLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
		return
	}
	defer r.Body.Close()

	if hasSensitiveField(body) {
		writeError(w, http.StatusBadRequest, "sensitive_field_forbidden", "passphrases, plaintext, and keys must not be sent to the API")
		return
	}

	var req cleanupSecretRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body does not match the cleanup secret contract")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}
	if req.JobID == "" || !validCleanupReason(req.Reason) {
		writeError(w, http.StatusBadRequest, "invalid_cleanup", "cleanup metadata is invalid")
		return
	}

	cleaned, err := s.secrets.Cleanup(r.Context(), id)
	if err != nil {
		s.writeSecretError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cleanupSecretResponse{
		ID:      id,
		Cleaned: cleaned,
	})
}

func (s Server) writeOpenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrInvalidAccess):
		writeError(w, http.StatusForbidden, "invalid_access", "access proof is invalid")
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
	case errors.Is(err, secrets.ErrInvalidAccess):
		writeError(w, http.StatusForbidden, "invalid_access", "access proof is invalid")
	case errors.Is(err, secrets.ErrPayloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "encrypted payload is too large")
	case errors.Is(err, secrets.ErrUnsupportedKind):
		writeError(w, http.StatusBadRequest, "unsupported_kind", "only text and file secrets are supported")
	case errors.Is(err, secrets.ErrUnsupportedViews):
		writeError(w, http.StatusBadRequest, "unsupported_max_views", "only one-time secrets are supported")
	case errors.Is(err, secrets.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", "secret is not pending upload")
	case errors.Is(err, secrets.ErrObjectMissing):
		writeError(w, http.StatusUnprocessableEntity, "object_missing", "uploaded object is missing or oversized")
	case errors.Is(err, secrets.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_secret", "secret metadata is invalid")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "request failed")
	}
}

func hashAccessProof(proof string) (string, error) {
	if proof == "" {
		return "", fmt.Errorf("access proof is required")
	}
	proofBytes, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || len(proofBytes) == 0 {
		return "", fmt.Errorf("invalid access proof")
	}
	sum := sha256.Sum256(proofBytes)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
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

func validCleanupReason(reason string) bool {
	switch reason {
	case "consumed", "expired", "orphan", "manual", "retry":
		return true
	default:
		return false
	}
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

func (s Server) createSecretBodyLimit() int64 {
	payloadLimit := s.secretsPayloadLimit()
	base64PayloadLimit := ((payloadLimit + 2) / 3) * 4
	return base64PayloadLimit + createBodyOverheadLimit
}
