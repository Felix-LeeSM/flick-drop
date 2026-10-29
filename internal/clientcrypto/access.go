// Package clientcrypto is the Go twin of web/src/lib/crypto/ for non-browser
// clients (the flick CLI), so either client can open what the other created.
//
// Secrets are age files (age.go, contracts/age-format-v1.md). This file holds
// the one Flick-specific value left: the Model A access proof, a PBKDF2 value
// that gates the open and cannot decrypt anything. Its constants are part of
// the wire contract; both this package and web/src/lib/crypto/access.test.ts
// check the access_proof block of tests/fixtures/client-crypto-vectors.json.
//
// The server never sees a passphrase, an identity, or plaintext — see
// docs/architecture/security-model.md.
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
	// KDFAlgorithm is the only algorithm the API contract accepts
	// (contracts/openapi.yaml KdfParams.algorithm).
	KDFAlgorithm = "PBKDF2-SHA-256"
	// KDFIterations is both the default and the server-enforced minimum.
	KDFIterations = 600_000
	// KDFMaxIterations bounds the work a stored KDF block can force on an
	// opener: ten times the default, a few seconds rather than hours.
	KDFMaxIterations = 6_000_000
	KeyLengthBits    = 256
	SaltBytes        = 16
	// AccessVerifierPurpose domain-separates the access proof from anything
	// else derived from the passphrase.
	AccessVerifierPurpose = "Flick access verifier v1"
)

// KDFParams is the wire form of the KDF block. Field names match
// contracts/openapi.yaml KdfParams exactly; the API rejects unknown fields.
type KDFParams struct {
	Algorithm     string `json:"algorithm"`
	Salt          string `json:"salt"`
	Iterations    int    `json:"iterations"`
	KeyLengthBits int    `json:"key_length_bits"`
}

// AccessVerifier is the Model A proof of passphrase knowledge. The server
// stores only a hash of Proof and never learns the passphrase.
type AccessVerifier struct {
	KDF   KDFParams `json:"kdf"`
	Proof string    `json:"proof"`
}

// ErrDecrypt is returned when no key opens an age file: a wrong passphrase or
// link key, or a file encrypted to someone else. Callers cannot tell these
// apart, and neither can the user.
var ErrDecrypt = errors.New("decryption failed: wrong passphrase or key, or the payload was altered")

// NewAccessVerifier builds the Model A create-time proof under a fresh salt.
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

// DeriveAccessProof recomputes the proof for an existing access KDF block, which
// is what `flick open` does with the block returned by GET /api/secrets/{id}.
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
	// The NUL byte is the domain separator, matching accessVerifierMaterial in
	// web/src/lib/crypto/access.ts. Concatenating without it would let a crafted
	// purpose-suffixed passphrase collide with a plain one.
	material := AccessVerifierPurpose + "\x00" + passphrase
	proof, err := pbkdf2.Key(sha256.New, material, salt, kdf.Iterations, KeyLengthBits/8)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(proof), nil
}

// ValidateKDF rejects KDF blocks that would weaken derivation. It mirrors
// assertKdf in web/src/lib/crypto/access.ts: these values arrive from the
// server, so a lowered iteration count has to be refused here, not trusted.
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
