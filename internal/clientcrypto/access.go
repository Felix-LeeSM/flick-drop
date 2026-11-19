// Package clientcrypto is the Go twin of web/src/lib/crypto/. The server never sees a
// passphrase, identity, or plaintext.
package clientcrypto

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	KDFAlgorithm = "PBKDF2-SHA-256"
	// Also the server-enforced minimum.
	KDFIterations = 600_000
	// Bounds the work a server-supplied KDF block can force on an opener.
	KDFMaxIterations      = 6_000_000
	KeyLengthBits         = 256
	SaltBytes             = 16
	AccessVerifierPurpose = "Flick access verifier v1"
)

// Field names match contracts/openapi.yaml KdfParams; the API rejects unknown fields.
type KDFParams struct {
	Algorithm     string `json:"algorithm"`
	Salt          string `json:"salt"`
	Iterations    int    `json:"iterations"`
	KeyLengthBits int    `json:"key_length_bits"`
}

// The server stores only a hash of Proof.
type AccessVerifier struct {
	KDF   KDFParams `json:"kdf"`
	Proof string    `json:"proof"`
}

var ErrDecrypt = errors.New("decryption failed: wrong passphrase or key, or the payload was altered")

func NewAccessVerifier(passphrase string) (AccessVerifier, error) {
	salt := make([]byte, SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return AccessVerifier{}, fmt.Errorf("could not read random bytes: %w", err)
	}
	kdf := KDFParams{
		Algorithm:     KDFAlgorithm,
		Salt:          base64.StdEncoding.EncodeToString(salt),
		Iterations:    KDFIterations,
		KeyLengthBits: KeyLengthBits,
	}
	proof, err := DeriveAccessProof(passphrase, kdf)
	if err != nil {
		return AccessVerifier{}, err
	}
	return AccessVerifier{KDF: kdf, Proof: proof}, nil
}

func DeriveAccessProof(passphrase string, kdf KDFParams) (string, error) {
	if err := ValidateKDF(kdf); err != nil {
		return "", err
	}
	if passphrase == "" {
		return "", errors.New("passphrase is required")
	}
	salt, err := base64.StdEncoding.DecodeString(kdf.Salt)
	if err != nil {
		return "", fmt.Errorf("access KDF salt is not valid base64: %w", err)
	}
	// NUL separator matches accessVerifierMaterial in web/src/lib/crypto/access.ts and blocks purpose/passphrase collisions.
	material := AccessVerifierPurpose + "\x00" + passphrase
	proof, err := pbkdf2.Key(sha256.New, material, salt, kdf.Iterations, KeyLengthBits/8)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(proof), nil
}

// Mirrors assertKdf in web/src/lib/crypto/access.ts; server-supplied values must not lower derivation.
func ValidateKDF(kdf KDFParams) error {
	switch {
	case kdf.Algorithm != KDFAlgorithm:
		return fmt.Errorf("unsupported KDF algorithm %q", kdf.Algorithm)
	case kdf.Iterations < KDFIterations:
		return fmt.Errorf("KDF iterations %d below the minimum %d", kdf.Iterations, KDFIterations)
	case kdf.Iterations > KDFMaxIterations:
		return fmt.Errorf("KDF iterations %d above the maximum %d", kdf.Iterations, KDFMaxIterations)
	case kdf.KeyLengthBits != KeyLengthBits:
		return fmt.Errorf("unsupported KDF key length %d", kdf.KeyLengthBits)
	case kdf.Salt == "":
		return errors.New("KDF salt is required")
	}
	return nil
}
