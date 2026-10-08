package requests

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

var (
	ErrInvalid     = errors.New("invalid request")
	ErrTooLarge    = errors.New("request payload too large")
	ErrUnavailable = errors.New("request unavailable")
	ErrConflict    = errors.New("request conflict")
)

const (
	EnvelopeMaxBytes = 4096
	MaxGeneration    = 16
	algorithm        = "RSA-OAEP-256+A256GCM"
	tagBytes         = 16
)

type Envelope struct {
	Version           int                `json:"version"`
	Algorithm         string             `json:"algorithm"`
	WrappedKey        string             `json:"wrapped_key"`
	Nonce             string             `json:"nonce"`
	EncryptedFilename *EncryptedFilename `json:"encrypted_filename,omitempty"`
}

type EncryptedFilename struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
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

func publicKeyFingerprint(s string) (string, error) {
	der, err := canonicalBase64(s, 1, 512)
	if err != nil {
		return "", err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", ErrInvalid
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok || rsaKey.N.BitLen() != 2048 || rsaKey.E != 65537 || rsaKey.N.Bit(0) != 1 {
		return "", ErrInvalid
	}
	canonical, err := x509.MarshalPKIXPublicKey(rsaKey)
	if err != nil || !bytes.Equal(der, canonical) {
		return "", ErrInvalid
	}
	fp := sha256.Sum256(der)
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
	var fields map[string]json.RawMessage
	if err := DecodeObject(raw, &fields, []string{"version", "algorithm", "wrapped_key", "nonce"}, []string{"encrypted_filename"}); err != nil {
		return nil, err
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil || e.Version != 1 || e.Algorithm != algorithm {
		return nil, ErrInvalid
	}
	if _, err := canonicalBase64(e.WrappedKey, 256, 256); err != nil {
		return nil, err
	}
	nonce, err := canonicalBase64(e.Nonce, 12, 12)
	if err != nil {
		return nil, err
	}
	if kind == "file" {
		var name EncryptedFilename
		if err := DecodeObject(fields["encrypted_filename"], &name, []string{"nonce", "ciphertext"}, nil); err != nil {
			return nil, err
		}
		filenameNonce, err := canonicalBase64(name.Nonce, 12, 12)
		if err != nil || bytes.Equal(nonce, filenameNonce) {
			return nil, ErrInvalid
		}
		if _, err := canonicalBase64(name.Ciphertext, 17, 1040); err != nil {
			return nil, err
		}
	} else if kind != "text" || e.EncryptedFilename != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(e)
}
