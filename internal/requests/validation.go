package requests

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"filippo.io/age"
)

var (
	ErrInvalid     = errors.New("invalid request")
	ErrTooLarge    = errors.New("request payload too large")
	ErrUnavailable = errors.New("request unavailable")
	ErrConflict    = errors.New("request conflict")
	ErrStorage     = errors.New("request object storage unavailable")
)

const (
	EnvelopeMaxBytes = 8192
	MaxGeneration    = 16
	// envelopeVersion 2 is the age format (contracts/age-format-v1.md). The
	// RSA-OAEP/AES-GCM version 1 is gone.
	envelopeVersion = 2
	// recipientPrefix is the only recipient type a request uses: the hybrid
	// ML-KEM-768 + X25519 recipient, about 1,960 characters.
	recipientPrefix     = "age1pq1"
	maxRecipientChars   = 2400
	maxEncryptedNameLen = 4096
	// AgeMagic starts every age file; bodies and filenames must carry it.
	AgeMagic = "age-encryption.org/v1\n"
)

// Envelope is the submitted metadata beside the body. For files it carries the
// filename as a base64 age file to the same recipient; text has none.
type Envelope struct {
	Version           int    `json:"version"`
	EncryptedFilename string `json:"encrypted_filename,omitempty"`
}

// DecodeObject rejects unknown/case-alias/duplicate fields, nulls, missing
// required fields, and trailing JSON before decoding the bounded object.
func DecodeObject(data []byte, target any, required, optional []string) error {
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return ErrInvalid
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range append(append([]string{}, required...), optional...) {
		allowed[key] = true
	}
	seen := make(map[string]bool, len(allowed))
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return ErrInvalid
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrInvalid
		}
		seen[key] = true
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	for _, key := range required {
		if !seen[key] {
			return ErrInvalid
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return ErrInvalid
	}
	return nil
}

func canonicalBase64(s string, min, max int) ([]byte, error) {
	if len(s) > base64.StdEncoding.EncodedLen(max) {
		return nil, ErrInvalid
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(b) < min || len(b) > max || base64.StdEncoding.EncodeToString(b) != s {
		return nil, ErrInvalid
	}
	return b, nil
}

// publicKeyFingerprint accepts a hybrid age recipient in its canonical string
// form and returns SHA-256 over that string, unpadded base64url.
func publicKeyFingerprint(s string) (string, error) {
	if len(s) > maxRecipientChars || !strings.HasPrefix(s, recipientPrefix) {
		return "", ErrInvalid
	}
	recipient, err := age.ParseHybridRecipient(s)
	if err != nil || recipient.String() != s {
		return "", ErrInvalid
	}
	fp := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(fp[:]), nil
}

func tokenHash(token string) ([]byte, error) {
	if len(token) != 43 {
		return nil, ErrInvalid
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != token {
		return nil, ErrInvalid
	}
	hash := sha256.Sum256(b)
	return hash[:], nil
}

func validateEnvelope(raw json.RawMessage, kind string) ([]byte, error) {
	if len(raw) > EnvelopeMaxBytes {
		return nil, ErrInvalid
	}
	var e Envelope
	if err := DecodeObject(raw, &e, []string{"version"}, []string{"encrypted_filename"}); err != nil {
		return nil, err
	}
	if e.Version != envelopeVersion {
		return nil, ErrInvalid
	}
	switch kind {
	case "file":
		name, err := canonicalBase64(e.EncryptedFilename, len(AgeMagic), maxEncryptedNameLen)
		if err != nil || !isAgeFile(name) {
			return nil, ErrInvalid
		}
	case "text":
		if e.EncryptedFilename != "" {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	return json.Marshal(e)
}

func isAgeFile(b []byte) bool {
	return bytes.HasPrefix(b, []byte(AgeMagic))
}
