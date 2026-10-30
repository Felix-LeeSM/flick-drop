package clientcrypto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The access_proof vector is shared with web/src/lib/crypto/access.test.ts. If
// either side's constants move, one of the two suites fails here instead of
// Model A links created by one client refusing the other's proof.
const vectorsPath = "../../tests/fixtures/client-crypto-vectors.json"

type vectors struct {
	AccessProof struct {
		Passphrase string    `json:"passphrase"`
		KDF        KDFParams `json:"kdf"`
		Proof      string    `json:"proof"`
	} `json:"access_proof"`
}

func TestGoldenVectorAccessProof(t *testing.T) {
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read golden vectors: %v", err)
	}
	var loaded vectors
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("parse golden vectors: %v", err)
	}
	v := loaded.AccessProof

	proof, err := DeriveAccessProof(v.Passphrase, v.KDF)
	if err != nil {
		t.Fatalf("derive access proof: %v", err)
	}
	if proof != v.Proof {
		t.Errorf("access proof drifted from the golden vector\n got: %s\nwant: %s", proof, v.Proof)
	}
}

func TestValidateKDFRejectsInvalidParams(t *testing.T) {
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
		"raised iterations":  func(k KDFParams) KDFParams { k.Iterations = KDFMaxIterations + 1; return k },
		"other algorithm":    func(k KDFParams) KDFParams { k.Algorithm = "PBKDF2-SHA-1"; return k },
		"shorter key":        func(k KDFParams) KDFParams { k.KeyLengthBits = 128; return k },
		"missing salt":       func(k KDFParams) KDFParams { k.Salt = ""; return k },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveAccessProof("passphrase", mutate(valid)); err == nil {
				t.Error("derived a proof under a weakened KDF block")
			}
		})
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
	if _, err := DeriveAccessProof("", verifier.KDF); err == nil {
		t.Error("derived a proof from an empty passphrase")
	}
}

func TestKeyFragmentRoundTrip(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	fragment := EncodeKeyFragment(id)
	if !strings.HasPrefix(fragment, FragmentKeyPrefix+IdentityPrefix) {
		t.Fatalf("fragment %q does not carry the identity", fragment)
	}
	for _, form := range []string{fragment, "#" + fragment} {
		decoded, err := DecodeKeyFragment(form)
		if err != nil {
			t.Fatalf("decode %q: %v", form, err)
		}
		if decoded.String() != id.String() {
			t.Error("decoded identity differs from the encoded one")
		}
	}
}

func TestDecodeKeyFragmentRejectsBadInput(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	valid := id.String()
	for name, fragment := range map[string]string{
		"no prefix":    "#token=" + valid,
		"empty key":    "#key=",
		"lowercase":    "#key=" + strings.ToLower(valid),
		"old raw key":  "#key=" + strings.Repeat("A", 43),
		"bad checksum": "#key=" + valid[:len(valid)-1] + map[bool]string{true: "Q", false: "P"}[valid[len(valid)-1] == 'P'],
		// Bounded before parsing so a huge fragment cannot force large work (#89).
		"oversized": "#key=" + IdentityPrefix + strings.Repeat("Q", maxKeyFragmentChars),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeKeyFragment(fragment); err == nil {
				t.Errorf("accepted %q", fragment)
			}
		})
	}
}
