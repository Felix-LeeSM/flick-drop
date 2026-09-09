package clientcrypto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// The golden vectors are the only thing standing between this package and
// silent divergence from web/src/lib/crypto/text.ts. Both sides read the same
// file: web/src/lib/crypto/vectors.test.ts asserts the browser reproduces these
// exact bytes. If either implementation's constants move, one of the two suites
// fails and names the vector that broke.
const vectorsPath = "../../tests/fixtures/client-crypto-vectors.json"

type vectors struct {
	TextModelA struct {
		Passphrase string    `json:"passphrase"`
		Plaintext  string    `json:"plaintext"`
		KDF        KDFParams `json:"kdf"`
		Nonce      string    `json:"nonce"`
		Ciphertext string    `json:"ciphertext"`
	} `json:"text_model_a"`
	AccessProof struct {
		Passphrase string    `json:"passphrase"`
		KDF        KDFParams `json:"kdf"`
		Proof      string    `json:"proof"`
	} `json:"access_proof"`
	TextModelB struct {
		Key        string `json:"key"`
		Plaintext  string `json:"plaintext"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
	} `json:"text_model_b"`
	FileModelB struct {
		Key               string `json:"key"`
		Plaintext         string `json:"plaintext"`
		Nonce             string `json:"nonce"`
		Ciphertext        string `json:"ciphertext"`
		Filename          string `json:"filename"`
		FilenameNonce     string `json:"filename_nonce"`
		EncryptedFilename string `json:"encrypted_filename"`
		ContentType       string `json:"content_type"`
	} `json:"file_model_b"`
	Fragment struct {
		Key      string `json:"key"`
		Fragment string `json:"fragment"`
	} `json:"fragment"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read golden vectors: %v", err)
	}
	var loaded vectors
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("parse golden vectors: %v", err)
	}
	return loaded
}

func decodeBase64(t *testing.T, label, value string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}
	return decoded
}

func TestGoldenVectorModelAText(t *testing.T) {
	v := loadVectors(t).TextModelA

	salt := decodeBase64(t, "salt", v.KDF.Salt)
	key, err := DeriveKey(v.Passphrase, salt, v.KDF.Iterations)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}

	payload, err := encryptWithNonce([]byte(v.Plaintext), key, v.KDF, decodeBase64(t, "nonce", v.Nonce))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if payload.Ciphertext != v.Ciphertext {
		t.Errorf("ciphertext drifted from the golden vector\n got: %s\nwant: %s", payload.Ciphertext, v.Ciphertext)
	}

	plaintext, err := DecryptWithPassphrase(
		Payload{Ciphertext: v.Ciphertext, Nonce: v.Nonce, KDF: v.KDF},
		v.Passphrase,
	)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != v.Plaintext {
		t.Errorf("plaintext = %q, want %q", plaintext, v.Plaintext)
	}
}

func TestGoldenVectorAccessProof(t *testing.T) {
	v := loadVectors(t).AccessProof

	proof, err := DeriveAccessProof(v.Passphrase, v.KDF)
	if err != nil {
		t.Fatalf("derive access proof: %v", err)
	}
	if proof != v.Proof {
		t.Errorf("access proof drifted from the golden vector\n got: %s\nwant: %s", proof, v.Proof)
	}
}

func TestGoldenVectorModelBText(t *testing.T) {
	v := loadVectors(t).TextModelB

	key := decodeBase64(t, "key", v.Key)
	payload, err := encryptWithNonce([]byte(v.Plaintext), key, KDFParams{}, decodeBase64(t, "nonce", v.Nonce))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if payload.Ciphertext != v.Ciphertext {
		t.Errorf("ciphertext drifted from the golden vector\n got: %s\nwant: %s", payload.Ciphertext, v.Ciphertext)
	}

	plaintext, err := Decrypt(Payload{Ciphertext: v.Ciphertext, Nonce: v.Nonce}, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != v.Plaintext {
		t.Errorf("plaintext = %q, want %q", plaintext, v.Plaintext)
	}
}

func TestGoldenVectorModelBFile(t *testing.T) {
	v := loadVectors(t).FileModelB

	key := decodeBase64(t, "key", v.Key)
	payload, err := encryptWithNonce([]byte(v.Plaintext), key, KDFParams{}, decodeBase64(t, "nonce", v.Nonce))
	if err != nil {
		t.Fatalf("encrypt file body: %v", err)
	}
	if payload.Ciphertext != v.Ciphertext {
		t.Errorf("file ciphertext drifted from the golden vector\n got: %s\nwant: %s", payload.Ciphertext, v.Ciphertext)
	}

	envelope, err := encryptFilenameWithNonce(v.Filename, key, decodeBase64(t, "filename nonce", v.FilenameNonce))
	if err != nil {
		t.Fatalf("encrypt filename: %v", err)
	}
	// Compared field by field, not as raw JSON: both sides parse the envelope,
	// so key order is not part of the contract and must not fail the vector.
	var got, want encryptedMetadata
	if err := json.Unmarshal([]byte(envelope), &got); err != nil {
		t.Fatalf("parse produced envelope: %v", err)
	}
	if err := json.Unmarshal([]byte(v.EncryptedFilename), &want); err != nil {
		t.Fatalf("parse golden envelope: %v", err)
	}
	if got != want {
		t.Errorf("filename envelope drifted from the golden vector\n got: %+v\nwant: %+v", got, want)
	}

	body, err := Decrypt(Payload{Ciphertext: v.Ciphertext, Nonce: v.Nonce}, key)
	if err != nil {
		t.Fatalf("decrypt file body: %v", err)
	}
	if string(body) != v.Plaintext {
		t.Errorf("file body = %q, want %q", body, v.Plaintext)
	}

	name, err := DecryptFilename(v.EncryptedFilename, key)
	if err != nil {
		t.Fatalf("decrypt filename: %v", err)
	}
	if name != v.Filename {
		t.Errorf("filename = %q, want %q", name, v.Filename)
	}
}

func TestGoldenVectorFragment(t *testing.T) {
	v := loadVectors(t).Fragment

	key := decodeBase64(t, "key", v.Key)
	if got := EncodeKeyFragment(key); got != v.Fragment {
		t.Errorf("fragment = %q, want %q", got, v.Fragment)
	}

	decoded, err := DecodeKeyFragment(v.Fragment)
	if err != nil {
		t.Fatalf("decode fragment: %v", err)
	}
	if base64.StdEncoding.EncodeToString(decoded) != v.Key {
		t.Errorf("decoded fragment key = %s, want %s", base64.StdEncoding.EncodeToString(decoded), v.Key)
	}
}
