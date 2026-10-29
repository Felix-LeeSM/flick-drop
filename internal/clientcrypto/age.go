package clientcrypto

// age.go pins how Flick uses age (c2sp.org/age) — see contracts/age-format-v1.md.
// Everything here is a standard age recipient, identity, or file; Flick adds no
// construction of its own. web/src/lib/crypto/age.ts is the browser twin, and
// tests/fixtures/age/ holds files each side produced for the other to open.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

const (
	// IdentityPrefix marks the only identity type a secret uses: the hybrid
	// ML-KEM-768 + X25519 identity. Body and filename are age files encrypted to
	// its recipient.
	IdentityPrefix = "AGE-SECRET-KEY-PQ-1"
	// WrapWorkFactor is age's own default scrypt work factor (log2 N) for the
	// Model A passphrase file that wraps the identity.
	WrapWorkFactor = 18
	// MaxWrapWorkFactor is the highest work factor a client will run. The
	// browser library refuses anything above 20, so a wrapped identity Go would
	// open must open in the browser too. The wrapped identity arrives from the
	// server; this cap stops it from pinning the client's CPU and memory.
	MaxWrapWorkFactor = 20
	// maxIdentityBytes bounds an unwrapped identity; a real one is 77 bytes.
	maxIdentityBytes = 1024
)

// NewIdentity generates the per-secret identity.
func NewIdentity() (*age.HybridIdentity, error) {
	return age.GenerateHybridIdentity()
}

// ParseIdentity reads an identity from a Model B fragment or an unwrapped
// Model A file. Any other identity type is refused.
func ParseIdentity(s string) (*age.HybridIdentity, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, IdentityPrefix) {
		return nil, fmt.Errorf("identity must start with %s", IdentityPrefix)
	}
	return age.ParseHybridIdentity(s)
}

// EncryptTo returns a writer that encrypts into dst for id's recipient. The
// caller must Close it to write the final chunk.
func EncryptTo(dst io.Writer, id *age.HybridIdentity) (io.WriteCloser, error) {
	return age.Encrypt(dst, id.Recipient())
}

// DecryptWith returns a reader of the plaintext in src. Each 64 KiB chunk is
// authenticated before it is returned, and a truncated file fails at the end.
func DecryptWith(src io.Reader, id *age.HybridIdentity) (io.Reader, error) {
	r, err := age.Decrypt(src, id)
	if err != nil {
		return nil, decryptError(err)
	}
	return r, nil
}

// WrapIdentity encrypts id under passphrase as a standard age passphrase file
// (Model A). The server stores the result and returns it only from a verified
// open.
func WrapIdentity(id *age.HybridIdentity, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase is required")
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	recipient.SetWorkFactor(WrapWorkFactor)
	var out bytes.Buffer
	w, err := age.Encrypt(&out, recipient)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, id.String()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// UnwrapIdentity opens a WrapIdentity file.
func UnwrapIdentity(wrapped []byte, passphrase string) (*age.HybridIdentity, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase is required")
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	identity.SetMaxWorkFactor(MaxWrapWorkFactor)
	r, err := age.Decrypt(bytes.NewReader(wrapped), identity)
	if err != nil {
		return nil, decryptError(err)
	}
	// An identity string is under 100 bytes; refuse to buffer a large file a
	// server substituted.
	plaintext, err := io.ReadAll(io.LimitReader(r, maxIdentityBytes+1))
	if err != nil {
		return nil, decryptError(err)
	}
	if len(plaintext) > maxIdentityBytes {
		return nil, errors.New("wrapped identity is too large")
	}
	return ParseIdentity(string(plaintext))
}

// decryptError maps "no identity matched" to ErrDecrypt, so a wrong passphrase
// or key reads the same as before. Other errors (a malformed header, a work
// factor over the cap, a corrupted chunk) keep their own message.
func decryptError(err error) error {
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		return ErrDecrypt
	}
	return err
}
