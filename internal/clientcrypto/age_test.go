package clientcrypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// tests/fixtures/age/vectors.json holds one block made by this package and one
// made by web/src/lib/crypto/age.ts. Both suites open both blocks, so a format
// choice that moves on one side (identity type, work factor, how the identity
// is wrapped) fails here or in web/src/lib/crypto/age.test.ts.
const ageFixtures = "../../tests/fixtures/age"

type ageSide struct {
	Identity        string `json:"identity"`
	WrappedIdentity string `json:"wrapped_identity"`
	Text            string `json:"text"`
	Filename        string `json:"filename"`
	File            string `json:"file"`
}

type ageVectors struct {
	Passphrase string `json:"passphrase"`
	Plaintext  struct {
		Text     string `json:"text"`
		Filename string `json:"filename"`
		File     struct {
			Length int `json:"length"`
		} `json:"file"`
	} `json:"plaintext"`
	Go  ageSide `json:"go"`
	Web ageSide `json:"web"`
}

func loadAgeVectors(t *testing.T) ageVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ageFixtures, "vectors.json"))
	if err != nil {
		t.Fatalf("read age vectors: %v", err)
	}
	var v ageVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse age vectors: %v", err)
	}
	return v
}

func patternFile(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func openAll(t *testing.T, src []byte, id *age.HybridIdentity) []byte {
	t.Helper()
	r, err := DecryptWith(bytes.NewReader(src), id)
	if err != nil {
		t.Fatalf("decrypt header: %v", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decrypt body: %v", err)
	}
	return plaintext
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return b
}

// ciphertextSize is the age payload arithmetic in contracts/age-format-v1.md:
// header, 16-byte nonce, plaintext, and one 16-byte tag per 64 KiB chunk (at
// least one chunk, even for an empty file).
func ciphertextSize(header, plaintext int) int {
	chunks := max(1, (plaintext+65535)/65536)
	return header + 16 + plaintext + 16*chunks
}

func headerLength(t *testing.T, file []byte) int {
	t.Helper()
	i := bytes.Index(file, []byte("\n--- "))
	if i < 0 {
		t.Fatal("no header MAC line")
	}
	end := bytes.IndexByte(file[i+1:], '\n')
	return i + 1 + end + 1
}

func TestAgeVectorsOpenFromBothClients(t *testing.T) {
	v := loadAgeVectors(t)
	for name, side := range map[string]ageSide{"go": v.Go, "web": v.Web} {
		t.Run(name, func(t *testing.T) {
			// Model A path: the passphrase recovers the identity.
			id, err := UnwrapIdentity(b64(t, side.WrappedIdentity), v.Passphrase)
			if err != nil {
				t.Fatalf("unwrap: %v", err)
			}
			if id.String() != side.Identity {
				t.Fatal("unwrapped identity differs from the fragment identity")
			}
			// Model B path: the fragment identity is used directly.
			fragmentID, err := ParseIdentity(side.Identity)
			if err != nil {
				t.Fatalf("parse identity: %v", err)
			}
			if got := string(openAll(t, b64(t, side.Text), fragmentID)); got != v.Plaintext.Text {
				t.Fatalf("text = %q", got)
			}
			if got := string(openAll(t, b64(t, side.Filename), id)); got != v.Plaintext.Filename {
				t.Fatalf("filename = %q", got)
			}
			file, err := os.ReadFile(filepath.Join(ageFixtures, side.File))
			if err != nil {
				t.Fatal(err)
			}
			want := patternFile(v.Plaintext.File.Length)
			if !bytes.Equal(openAll(t, file, id), want) {
				t.Fatal("multi-chunk file body differs")
			}
			if got := ciphertextSize(headerLength(t, file), len(want)); got != len(file) {
				t.Fatalf("size formula = %d, file is %d bytes", got, len(file))
			}
		})
	}
}

func TestAgeRoundTripAndSizes(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id.String(), IdentityPrefix) || len(id.String()) > 100 {
		t.Fatalf("identity %q is not a short hybrid identity", id.String())
	}
	for _, n := range []int{0, 1, 65535, 65536, 65537, 3*65536 + 7} {
		var out bytes.Buffer
		w, err := EncryptTo(&out, id)
		if err != nil {
			t.Fatal(err)
		}
		plaintext := patternFile(n)
		if _, err := w.Write(plaintext); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if got := ciphertextSize(headerLength(t, out.Bytes()), n); got != out.Len() {
			t.Fatalf("n=%d: size formula = %d, ciphertext is %d bytes", n, got, out.Len())
		}
		if !bytes.Equal(openAll(t, out.Bytes(), id), plaintext) {
			t.Fatalf("n=%d: round trip differs", n)
		}
	}
}

func TestAgeFailsClosed(t *testing.T) {
	v := loadAgeVectors(t)
	wrapped := b64(t, v.Web.WrappedIdentity)

	if _, err := UnwrapIdentity(wrapped, "wrong passphrase"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong passphrase: err = %v, want ErrDecrypt", err)
	}

	other, _ := NewIdentity()
	if _, err := DecryptWith(bytes.NewReader(b64(t, v.Web.Text)), other); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong identity: err = %v, want ErrDecrypt", err)
	}

	if _, err := ParseIdentity("AGE-SECRET-KEY-1QQQQ"); err == nil {
		t.Fatal("a non-hybrid identity was accepted")
	}

	// A truncated body must error, not return a short plaintext.
	file, _ := os.ReadFile(filepath.Join(ageFixtures, v.Go.File))
	id, _ := ParseIdentity(v.Go.Identity)
	r, err := DecryptWith(bytes.NewReader(file[:len(file)-100]), id)
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		t.Fatal("a truncated file decrypted")
	}

	// A wrapped identity whose stanza asks for more than the cap is refused
	// before scrypt runs. Editing the stanza breaks the header MAC, but the
	// work factor is checked first, which is the point.
	heavy := bytes.Replace(wrapped, []byte(" 18\n"), []byte(" 21\n"), 1)
	if bytes.Equal(heavy, wrapped) {
		t.Fatal("fixture stanza does not carry work factor 18")
	}
	if _, err := UnwrapIdentity(heavy, v.Passphrase); err == nil || errors.Is(err, ErrDecrypt) {
		t.Fatalf("work factor 21: err = %v, want a work-factor error", err)
	}
}
