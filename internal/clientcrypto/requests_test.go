package clientcrypto

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseRecipientRoundTripsAFreshIdentity(t *testing.T) {
	withTimeout(t, time.Minute)
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recipient := RecipientStringOf(identity)

	parsed, err := ParseRecipient(recipient)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.String() != recipient {
		t.Errorf("round trip = %q", parsed.String())
	}
	if fp := RecipientFingerprint(recipient); len(fp) != 43 || strings.ContainsAny(fp, "+/=") {
		t.Errorf("fingerprint = %q, want 43 unpadded base64url chars", fp)
	}
}

func TestParseRecipientRejectsSubstitutedForms(t *testing.T) {
	withTimeout(t, time.Minute)
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recipient := RecipientStringOf(identity)
	// An uppercase bech32 encoding decodes but does not re-encode identically.
	upper := "AGE1PQ1" + recipient[len("age1pq1"):]

	for name, value := range map[string]string{
		"empty":        "",
		"wrong prefix": "age1qq" + recipient[len("age1pq1"):],
		"uppercase":    upper,
		"truncated":    recipient[:100],
	} {
		if _, err := ParseRecipient(value); err == nil {
			t.Errorf("accepted %q (%s)", value, name)
		}
	}
}

func TestEncryptToRecipientDecryptsWithTheMatchingIdentity(t *testing.T) {
	withTimeout(t, time.Minute)
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}

	var sealed bytes.Buffer
	w, err := EncryptToRecipient(&sealed, RecipientStringOf(identity))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := DecryptWith(bytes.NewReader(sealed.Bytes()), identity)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "reply" {
		t.Errorf("plain = %q", plain)
	}

	other, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptWith(bytes.NewReader(sealed.Bytes()), other); err == nil {
		t.Error("a different identity decrypted the note")
	}
}

func TestNewAttemptTokenMatchesTheCapabilityShape(t *testing.T) {
	withTimeout(t, time.Minute)
	token, err := NewAttemptToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || strings.ContainsAny(token, "+/=") {
		t.Errorf("token = %q, want 43 unpadded base64url chars", token)
	}
	again, _ := NewAttemptToken()
	if token == again {
		t.Error("two attempt tokens collided")
	}
}
