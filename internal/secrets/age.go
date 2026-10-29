package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"time"
)

// OpenStreamTimeout bounds how long an opened S3 body may stream to the
// recipient: 256 MiB at about 1.2 Mbit/s. The open handler enforces it as the
// write deadline.
const OpenStreamTimeout = 30 * time.Minute

// openObjectGrace is how long reconciliation leaves a consumed object alone,
// so it never deletes a body that is still streaming.
const openObjectGrace = OpenStreamTimeout + 5*time.Minute

// Size arithmetic for age files (contracts/age-format-v1.md). The server never
// decrypts; it only bounds what it stores and signs. A body's exact length is
// declared by the client, and these bounds decide what length is acceptable for
// a given plaintext limit.
const (
	// AgeHeaderAllowanceBytes bounds the header of a body or filename file. The
	// hybrid recipient header Flick clients write is 1,627 bytes.
	AgeHeaderAllowanceBytes = 4096
	ageChunkBytes           = 64 * 1024
	ageTagBytes             = 16
	agePayloadNonceBytes    = 16
	// maxWrappedIdentityBytes bounds the Model A scrypt file around a 77-byte
	// identity (about 230 bytes in practice).
	maxWrappedIdentityBytes = 1024
	// maxEncryptedFilenameBytes bounds the filename file: one hybrid header plus
	// a filename of a few hundred bytes.
	maxEncryptedFilenameBytes = 8 * 1024
	// maxContentTypeBytes bounds the media type returned as a response header.
	maxContentTypeBytes = 255
)

var ageMagic = []byte("age-encryption.org/v1\n")

// MaxCiphertextBytes is the largest age file a plaintext of n bytes can produce
// within the header allowance: header, payload nonce, n, and one tag per 64 KiB
// chunk (at least one).
func MaxCiphertextBytes(n int64) int64 {
	chunks := max(1, (n+ageChunkBytes-1)/ageChunkBytes)
	return AgeHeaderAllowanceBytes + agePayloadNonceBytes + n + ageTagBytes*chunks
}

// MaxPlaintextBytes is the largest plaintext whose age file always fits in c
// bytes, or 0 when not even an empty one does. It inverts MaxCiphertextBytes.
func MaxPlaintextBytes(c int64) int64 {
	if c < MaxCiphertextBytes(0) {
		return 0
	}
	// Start below the answer (counting one tag per started chunk of c) and
	// step up; the estimate is off by at most a few bytes.
	n := max(0, c-AgeHeaderAllowanceBytes-agePayloadNonceBytes-ageTagBytes*((c+ageChunkBytes-1)/ageChunkBytes))
	for MaxCiphertextBytes(n+1) <= c {
		n++
	}
	return n
}

var errNotAgeFile = errors.New("not an age file")

// decodeAgeField decodes a base64 age file carried in an API field and checks
// its length and magic line. It cannot check anything a client would need a
// key for.
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
