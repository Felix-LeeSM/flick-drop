package flickcli

import (
	"strings"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

func TestParseShareLinkModelB(t *testing.T) {
	key := make([]byte, clientcrypto.RawKeyBytes)
	for i := range key {
		key[i] = byte(i)
	}
	raw := "https://flick.example.com/s/abc123#" + clientcrypto.EncodeKeyFragment(key)

	link, err := ParseShareLink(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if link.Origin != "https://flick.example.com" {
		t.Errorf("origin = %q", link.Origin)
	}
	if link.ID != "abc123" {
		t.Errorf("id = %q", link.ID)
	}
	if string(link.Key) != string(key) {
		t.Error("fragment key did not survive parsing")
	}
}

func TestParseShareLinkModelACarriesNoKey(t *testing.T) {
	link, err := ParseShareLink("https://flick.example.com/s/abc123")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if link.Key != nil {
		t.Error("a link without a fragment produced a key")
	}
}

func TestParseShareLinkBareID(t *testing.T) {
	link, err := ParseShareLink("  abc123  ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if link.ID != "abc123" {
		t.Errorf("id = %q", link.ID)
	}
	if link.Origin != "" {
		t.Errorf("a bare ID produced origin %q", link.Origin)
	}
}

// A deployment reachable under a path prefix still has to resolve to the right
// API base, since /api sits beside /s on the same origin.
func TestParseShareLinkKeepsPathPrefix(t *testing.T) {
	link, err := ParseShareLink("https://example.com/flick/s/abc123")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if link.Origin != "https://example.com/flick" {
		t.Errorf("origin = %q, want https://example.com/flick", link.Origin)
	}
}

func TestParseShareLinkRejectsBadInput(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":            "",
		"blank":            "   ",
		"no share path":    "https://flick.example.com/abc123",
		"no id":            "https://flick.example.com/s/",
		"extra segments":   "https://flick.example.com/s/abc123/raw",
		"no host":          "https:///s/abc123",
		"path but no host": "/s/abc123",
		"bad fragment":     "https://flick.example.com/s/abc123#key=!!!",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseShareLink(raw); err == nil {
				t.Errorf("accepted %q", raw)
			}
		})
	}
}

func TestBuildShareLinkRoundTrips(t *testing.T) {
	key := make([]byte, clientcrypto.RawKeyBytes)
	built := BuildShareLink("https://flick.example.com/", "abc123", key)

	if strings.Count(built, "//") != 1 {
		t.Errorf("link %q has a doubled slash", built)
	}
	link, err := ParseShareLink(built)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if link.ID != "abc123" || string(link.Key) != string(key) {
		t.Errorf("round trip lost data: %+v", link)
	}
}

func TestBuildShareLinkOmitsKeyForModelA(t *testing.T) {
	built := BuildShareLink("https://flick.example.com", "abc123", nil)
	if strings.Contains(built, "#") {
		t.Errorf("model A link %q carries a fragment", built)
	}
}
