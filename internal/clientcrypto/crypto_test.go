package clientcrypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestModelARoundTrip(t *testing.T) {
	key, kdf, err := NewKDF("a passphrase")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	payload, err := Encrypt([]byte("db password"), key, kdf)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(payload.Ciphertext, "db password") {
		t.Error("ciphertext contains the plaintext")
	}
	if payload.SizeBytes != len("db password") {
		t.Errorf("size_bytes = %d, want %d", payload.SizeBytes, len("db password"))
	}

	plaintext, err := DecryptWithPassphrase(payload, "a passphrase")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != "db password" {
		t.Errorf("plaintext = %q", plaintext)
	}
}

func TestWrongPassphraseFailsAuthentication(t *testing.T) {
	key, kdf, err := NewKDF("right")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	payload, err := Encrypt([]byte("secret"), key, kdf)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = DecryptWithPassphrase(payload, "wrong")
	if !errors.Is(err, ErrDecrypt) {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestModelBRoundTripWithRandomKey(t *testing.T) {
	key, err := NewRandomKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if len(key) != RawKeyBytes {
		t.Fatalf("key length = %d, want %d", len(key), RawKeyBytes)
	}

	// Model B sends no KDF at all; the zero block is what the API client omits.
	payload, err := Encrypt([]byte("link bearer secret"), key, KDFParams{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if payload.KDF.Algorithm != "" {
		t.Errorf("model B payload carries a KDF: %+v", payload.KDF)
	}

	plaintext, err := Decrypt(payload, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != "link bearer secret" {
		t.Errorf("plaintext = %q", plaintext)
	}
}

func TestNoncesDifferBetweenEncryptions(t *testing.T) {
	key, err := NewRandomKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	first, err := Encrypt([]byte("same"), key, KDFParams{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	second, err := Encrypt([]byte("same"), key, KDFParams{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Reusing a nonce under one AES-GCM key breaks confidentiality outright,
	// so this is a correctness guard, not a style check.
	if first.Nonce == second.Nonce {
		t.Error("two encryptions reused the same nonce")
	}
}

// A KDF block arrives from the server. Accepting a lowered iteration count
// would let a compromised server force a cheap-to-brute-force derivation.
func TestValidateKDFRejectsDowngrades(t *testing.T) {
	valid := KDFParams{
		Algorithm:     KDFAlgorithm,
		Salt:          base64.StdEncoding.EncodeToString(make([]byte, SaltBytes)),
		Iterations:    KDFIterations,
		KeyLengthBits: KeyLengthBits,
	}
	if err := ValidateKDF(valid); err != nil {
		t.Fatalf("valid KDF rejected: %v", err)
	}

	tests := map[string]func(KDFParams) KDFParams{
		"lowered iterations": func(k KDFParams) KDFParams { k.Iterations = 1000; return k },
		"other algorithm":    func(k KDFParams) KDFParams { k.Algorithm = "PBKDF2-SHA-1"; return k },
		"shorter key":        func(k KDFParams) KDFParams { k.KeyLengthBits = 128; return k },
		"missing salt":       func(k KDFParams) KDFParams { k.Salt = ""; return k },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateKDF(mutate(valid)); err == nil {
				t.Error("accepted a weakened KDF block")
			}
		})
	}
}

func TestDeriveKeyRejectsWeakParameters(t *testing.T) {
	if _, err := DeriveKey("", make([]byte, SaltBytes), KDFIterations); err == nil {
		t.Error("accepted an empty passphrase")
	}
	if _, err := DeriveKey("passphrase", make([]byte, SaltBytes), KDFIterations-1); err == nil {
		t.Error("accepted iterations below the minimum")
	}
}

// The access proof must not be derivable from the encryption key or vice
// versa: the server stores a hash of the proof, and a proof that leaked the key
// would let the server decrypt what it stores.
func TestAccessProofIsDomainSeparatedFromTheKey(t *testing.T) {
	verifier, err := NewAccessVerifier("shared passphrase")
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	salt, err := base64.StdEncoding.DecodeString(verifier.KDF.Salt)
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	key, err := DeriveKey("shared passphrase", salt, verifier.KDF.Iterations)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	if verifier.Proof == base64.StdEncoding.EncodeToString(key) {
		t.Error("access proof equals the encryption key derived from the same passphrase and salt")
	}
}

func TestAccessProofIsStableAndPassphraseBound(t *testing.T) {
	verifier, err := NewAccessVerifier("passphrase")
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	again, err := DeriveAccessProof("passphrase", verifier.KDF)
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if again != verifier.Proof {
		t.Error("re-deriving the proof from the same passphrase and KDF gave a different value")
	}

	other, err := DeriveAccessProof("different", verifier.KDF)
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if other == verifier.Proof {
		t.Error("a different passphrase produced the same proof")
	}
}

func TestFilenameEnvelopeRoundTrip(t *testing.T) {
	key, err := NewRandomKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	envelope, err := EncryptFilename("payroll 2026.xlsx", key)
	if err != nil {
		t.Fatalf("encrypt filename: %v", err)
	}
	if strings.Contains(envelope, "payroll") {
		t.Error("envelope leaks the filename")
	}

	name, err := DecryptFilename(envelope, key)
	if err != nil {
		t.Fatalf("decrypt filename: %v", err)
	}
	if name != "payroll 2026.xlsx" {
		t.Errorf("filename = %q", name)
	}
}

func TestDecryptFilenameRejectsGarbage(t *testing.T) {
	key, err := NewRandomKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	for name, envelope := range map[string]string{
		"empty":          "",
		"not json":       "quarterly-report.pdf",
		"missing fields": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecryptFilename(envelope, key); err == nil {
				t.Error("accepted a malformed envelope")
			}
		})
	}
}

func TestKeyFragmentRoundTrip(t *testing.T) {
	key, err := NewRandomKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	fragment := EncodeKeyFragment(key)
	if !strings.HasPrefix(fragment, FragmentKeyPrefix) {
		t.Fatalf("fragment = %q, want a %q prefix", fragment, FragmentKeyPrefix)
	}
	// base64url only, so a share link never needs percent-encoding.
	encoded := strings.TrimPrefix(fragment, FragmentKeyPrefix)
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("fragment key %q is not URL-safe base64", encoded)
	}

	decoded, err := DecodeKeyFragment("#" + fragment)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != string(key) {
		t.Error("fragment round trip changed the key")
	}
}

func TestDecodeKeyFragmentRejectsBadInput(t *testing.T) {
	for name, fragment := range map[string]string{
		"no prefix":     "#token=abc",
		"empty key":     "#key=",
		"not base64url": "#key=!!!!",
		// Bounded before decoding so a huge fragment cannot force a large
		// allocation (#89).
		"oversized": "#key=" + strings.Repeat("A", 200),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeyFragment(fragment); err == nil {
				t.Error("accepted a bad fragment")
			}
		})
	}
}
