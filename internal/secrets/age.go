package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"time"
)

// 256 MiB at about 1.2 Mbit/s; the open handler enforces it as the write deadline.
const OpenStreamTimeout = 30 * time.Minute

// Reconciliation must never delete a body that is still streaming.
const openObjectGrace = OpenStreamTimeout + 5*time.Minute

// contracts/age-format-v1.md. The server never decrypts; it only bounds what it stores and signs.
const (
	// The hybrid recipient header Flick clients write is 1,627 bytes.
	AgeHeaderAllowanceBytes = 4096
	ageChunkBytes           = 64 * 1024
	ageTagBytes             = 16
	agePayloadNonceBytes    = 16
	// A 77-byte identity's scrypt file is about 230 bytes.
	maxWrappedIdentityBytes   = 1024
	maxEncryptedFilenameBytes = 8 * 1024
	maxContentTypeBytes       = 255
)

var ageMagic = []byte("age-encryption.org/v1\n")

// Header, payload nonce, n, and one tag per 64 KiB chunk (at least one).
func MaxCiphertextBytes(n int64) int64 {
	chunks := max(1, (n+ageChunkBytes-1)/ageChunkBytes)
	return AgeHeaderAllowanceBytes + agePayloadNonceBytes + n + ageTagBytes*chunks
}

// Inverts MaxCiphertextBytes; 0 when not even an empty file fits.
func MaxPlaintextBytes(c int64) int64 {
	if c < MaxCiphertextBytes(0) {
		return 0
	}
	// Start below the answer and step up; the estimate is off by at most a few bytes.
	n := max(0, c-AgeHeaderAllowanceBytes-agePayloadNonceBytes-ageTagBytes*((c+ageChunkBytes-1)/ageChunkBytes))
	for MaxCiphertextBytes(n+1) <= c {
		n++
	}
	return n
}

var errNotAgeFile = errors.New("not an age file")

func decodeAgeField(value string, limit int) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(value)) > limit+3 {
		return nil, errNotAgeFile
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) > limit || !bytes.HasPrefix(raw, ageMagic) {
		return nil, errNotAgeFile
	}
	return raw, nil
}
