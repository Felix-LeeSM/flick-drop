// Package clientcrypto mirrors the browser-side encryption in
// web/src/lib/crypto/text.ts so a non-browser client (the flick CLI) can create
// and open secrets the web app can also create and open.
//
// This is a deliberate second implementation of the same primitives. Every
// constant here is part of the on-the-wire contract: change one on this side
// only and links created by one client stop opening in the other, with nothing
// but an AEAD tag mismatch to explain it. The shared golden vectors in
// tests/fixtures/client-crypto-vectors.json are checked by both this package
// and web/src/lib/crypto/vectors.test.ts, and exist to make that drift fail
// loudly at the exact constant that moved.
//
// The server never sees a passphrase, a derived key, or plaintext — see
// docs/architecture/security-model.md.
package clientcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// KDFAlgorithm is the only algorithm the API contract accepts
	// (contracts/openapi.yaml KdfParams.algorithm).
	KDFAlgorithm = "PBKDF2-SHA-256"
	// KDFIterations is both the default and the server-enforced minimum.
	KDFIterations = 600_000
	KeyLengthBits = 256
	SaltBytes     = 16
	NonceBytes    = 12
	// RawKeyBytes is the Model B key size: 256 bits of randomness carried in
	// the share URL fragment instead of derived from a passphrase.
	RawKeyBytes = 32
	// AccessVerifierPurpose domain-separates the access proof from the
	// encryption key so the proof sent to the server cannot decrypt anything.
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

// Payload is one encrypted blob plus everything needed to decrypt it, minus the
// key. SizeBytes is the plaintext length, which is what the server records and
// what the presigned upload's signed Content-Length is derived from.
type Payload struct {
	Ciphertext string    `json:"ciphertext"`
	Nonce      string    `json:"nonce"`
	KDF        KDFParams `json:"kdf"`
	SizeBytes  int       `json:"size_bytes"`
}

// AccessVerifier is the Model A proof of passphrase knowledge. The server
// stores only a hash of Proof and never learns the passphrase.
type AccessVerifier struct {
	KDF   KDFParams `json:"kdf"`
	Proof string    `json:"proof"`
}

// encryptedMetadata is the JSON envelope carried in the `encrypted_filename`
// field. It is a JSON string inside the request, not a nested object, because
// that is the shape the browser produces (JSON.stringify in text.ts).
type encryptedMetadata struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// ErrDecrypt is returned whenever AES-GCM authentication fails. Callers cannot
// distinguish a wrong passphrase from a corrupted payload, and neither can the
// user — that is a property of AEAD, not a missing detail.
var ErrDecrypt = errors.New("decryption failed: wrong passphrase or key, or the payload was altered")

// DeriveKey turns a passphrase into the AES-GCM key (Model A). iterations is
// taken from the caller so an opened secret re-derives with the exact
// parameters it was created with.
func DeriveKey(passphrase string, salt []byte, iterations int) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase is required")
	}
	if iterations < KDFIterations {
		return nil, fmt.Errorf("PBKDF2 iterations below the minimum %d", KDFIterations)
	}
	return pbkdf2.Key(sha256.New, passphrase, salt, iterations, KeyLengthBits/8)
}

// NewKDF derives a key from a passphrase under a fresh random salt and returns
// the key with the KDF block describing how it was derived.
func NewKDF(passphrase string) ([]byte, KDFParams, error) {
	salt, err := randomBytes(SaltBytes)
	if err != nil {
		return nil, KDFParams{}, err
	}
	key, err := DeriveKey(passphrase, salt, KDFIterations)
	if err != nil {
		return nil, KDFParams{}, err
	}
	return key, KDFParams{
		Algorithm:     KDFAlgorithm,
		Salt:          base64.StdEncoding.EncodeToString(salt),
		Iterations:    KDFIterations,
		KeyLengthBits: KeyLengthBits,
	}, nil
}

// NewRandomKey generates the Model B key. It is never sent to the server; it
// travels in the URL fragment, which browsers do not transmit.
func NewRandomKey() ([]byte, error) {
	return randomBytes(RawKeyBytes)
}

// Encrypt seals plaintext under key. kdf is echoed into the returned payload
// unchanged: Model A passes the block from NewKDF, Model B passes the zero
// value, which the API client then omits from the request.
func Encrypt(plaintext, key []byte, kdf KDFParams) (Payload, error) {
	nonce, err := randomBytes(NonceBytes)
	if err != nil {
		return Payload{}, err
	}
	return encryptWithNonce(plaintext, key, kdf, nonce)
}

func encryptWithNonce(plaintext, key []byte, kdf KDFParams, nonce []byte) (Payload, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return Payload{}, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return Payload{
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		KDF:        kdf,
		SizeBytes:  len(plaintext),
	}, nil
}

// Decrypt opens a payload with an already-derived key.
func Decrypt(payload Payload, key []byte) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce is not valid base64: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ciphertext is not valid base64: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	// Checked, not assumed: crypto/cipher panics rather than erroring on a
	// wrong-length nonce, and this nonce comes off the wire. A malformed
	// response must fail the command, not crash it.
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce is %d bytes, want %d", len(nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// KeyFromPayload re-derives the Model A key from the payload's own KDF block.
// That block arrives from the server, so it is validated before use — a lowered
// iteration count is a downgrade attempt, not a formatting quirk.
//
// Callers that also need the key afterwards (to decrypt a filename, say) use
// this directly; DecryptWithPassphrase is the one-shot form.
func KeyFromPayload(payload Payload, passphrase string) ([]byte, error) {
	if err := ValidateKDF(payload.KDF); err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(payload.KDF.Salt)
	if err != nil {
		return nil, fmt.Errorf("KDF salt is not valid base64: %w", err)
	}
	return DeriveKey(passphrase, salt, payload.KDF.Iterations)
}

// DecryptWithPassphrase opens a Model A payload in one step.
func DecryptWithPassphrase(payload Payload, passphrase string) ([]byte, error) {
	key, err := KeyFromPayload(payload, passphrase)
	if err != nil {
		return nil, err
	}
	return Decrypt(payload, key)
}

// EncryptFilename seals a filename under the payload key and returns the JSON
// envelope the API stores in `encrypted_filename`. The filename is as sensitive
// as the file, so it never travels in the clear.
func EncryptFilename(name string, key []byte) (string, error) {
	nonce, err := randomBytes(NonceBytes)
	if err != nil {
		return "", err
	}
	return encryptFilenameWithNonce(name, key, nonce)
}

func encryptFilenameWithNonce(name string, key, nonce []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	envelope := encryptedMetadata{
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, []byte(name), nil)),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DecryptFilename opens the `encrypted_filename` envelope.
func DecryptFilename(envelope string, key []byte) (string, error) {
	if envelope == "" {
		return "", errors.New("encrypted filename is required")
	}
	var metadata encryptedMetadata
	if err := json.Unmarshal([]byte(envelope), &metadata); err != nil {
		return "", fmt.Errorf("encrypted filename is not a valid envelope: %w", err)
	}
	name, err := Decrypt(Payload{Ciphertext: metadata.Ciphertext, Nonce: metadata.Nonce}, key)
	if err != nil {
		return "", err
	}
	return string(name), nil
}

// NewAccessVerifier builds the Model A create-time proof under a fresh salt.
// This salt is independent of the encryption salt: the server stores the access
// KDF and hands it back on open, and reusing the encryption salt would let a
// proof-holder narrow the key derivation.
func NewAccessVerifier(passphrase string) (AccessVerifier, error) {
	salt, err := randomBytes(SaltBytes)
	if err != nil {
		return AccessVerifier{}, err
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
	// web/src/lib/crypto/text.ts. Concatenating without it would let a crafted
	// purpose-suffixed passphrase collide with a plain one.
	material := AccessVerifierPurpose + "\x00" + passphrase
	proof, err := pbkdf2.Key(sha256.New, material, salt, kdf.Iterations, KeyLengthBits/8)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(proof), nil
}

// ValidateKDF rejects KDF blocks that would weaken derivation. It mirrors
// assertKdf in web/src/lib/crypto/text.ts: these values arrive from the server,
// so a lowered iteration count has to be refused here, not trusted.
func ValidateKDF(kdf KDFParams) error {
	switch {
	case kdf.Algorithm != KDFAlgorithm:
		return fmt.Errorf("unsupported KDF algorithm %q", kdf.Algorithm)
	case kdf.Iterations < KDFIterations:
		return fmt.Errorf("KDF iterations %d below the minimum %d", kdf.Iterations, KDFIterations)
	case kdf.KeyLengthBits != KeyLengthBits:
		return fmt.Errorf("unsupported KDF key length %d", kdf.KeyLengthBits)
	case kdf.Salt == "":
		return errors.New("KDF salt is required")
	}
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("invalid AES key: %w", err)
	}
	return cipher.NewGCM(block)
}

func randomBytes(length int) ([]byte, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("could not read random bytes: %w", err)
	}
	return buf, nil
}
