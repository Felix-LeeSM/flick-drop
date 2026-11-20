package clientcrypto

// Go twin of web/src/lib/crypto/age.ts (contracts/age-format-v1.md); tests/fixtures/age/ holds files each side made.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

const (
	IdentityPrefix = "AGE-SECRET-KEY-PQ-1"
	// age's default scrypt work factor (log2 N).
	WrapWorkFactor = 18
	// The browser age library refuses above 20; also caps the CPU/memory a server-supplied wrapped identity can pin.
	MaxWrapWorkFactor = 20
	// A real identity is 77 bytes.
	maxIdentityBytes = 1024
)

func NewIdentity() (*age.HybridIdentity, error) {
	return age.GenerateHybridIdentity()
}

func ParseIdentity(s string) (*age.HybridIdentity, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, IdentityPrefix) {
		return nil, fmt.Errorf("identity must start with %s", IdentityPrefix)
	}
	return age.ParseHybridIdentity(s)
}

// The caller must Close it to write the final chunk.
func EncryptTo(dst io.Writer, id *age.HybridIdentity) (io.WriteCloser, error) {
	return age.Encrypt(dst, id.Recipient())
}

func DecryptWith(src io.Reader, id *age.HybridIdentity) (io.Reader, error) {
	r, err := age.Decrypt(src, id)
	if err != nil {
		return nil, decryptError(err)
	}
	return r, nil
}

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
	plaintext, err := io.ReadAll(io.LimitReader(r, maxIdentityBytes+1))
	if err != nil {
		return nil, decryptError(err)
	}
	if len(plaintext) > maxIdentityBytes {
		return nil, errors.New("wrapped identity is too large")
	}
	return ParseIdentity(string(plaintext))
}

func decryptError(err error) error {
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		return ErrDecrypt
	}
	return err
}
