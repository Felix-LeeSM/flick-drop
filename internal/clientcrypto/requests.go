package clientcrypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"

	"filippo.io/age"
)

// Request-link crypto, the Go twin of web/src/lib/crypto/requests.ts: the
// submitter holds only the recipient string, the requester only the identity.

// The hybrid recipient is about 1,960 characters; the API caps it at 2,400.
const MaxRecipientChars = 2400

var recipientPattern = regexp.MustCompile(`^age1pq1[02-9ac-hj-np-z]+$`)

// ParseRecipient accepts only a canonical lowercase recipient that re-encodes
// to itself, the same bar the API applies (internal/requests/validation.go).
func ParseRecipient(value string) (*age.HybridRecipient, error) {
	if len(value) > MaxRecipientChars || !recipientPattern.MatchString(value) {
		return nil, errors.New("not an age hybrid recipient (age1pq1…)")
	}
	parsed, err := age.ParseHybridRecipient(value)
	if err != nil {
		return nil, err
	}
	if parsed.String() != value {
		return nil, errors.New("recipient is not canonically encoded")
	}
	return parsed, nil
}

// RecipientStringOf is the create-side helper: the public form of a fresh identity.
func RecipientStringOf(identity *age.HybridIdentity) string {
	return identity.Recipient().String()
}

// SHA-256 over the exact recipient string, unpadded base64url, as the API stores it.
func RecipientFingerprint(recipient string) string {
	sum := sha256.Sum256([]byte(recipient))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// EncryptToRecipient seals to a recipient string the way a submitter must:
// without ever holding the matching identity.
func EncryptToRecipient(dst io.Writer, recipient string) (io.WriteCloser, error) {
	parsed, err := ParseRecipient(recipient)
	if err != nil {
		return nil, err
	}
	return age.Encrypt(dst, parsed)
}

// NewAttemptToken is the 32-byte base64url attempt token the API requires on
// every submit, upload, finalize, and abandon call.
func NewAttemptToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate attempt token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
