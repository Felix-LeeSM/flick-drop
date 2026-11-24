package flickcli

import (
	"strings"
	"testing"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

func requestLinkPair(t *testing.T) (submission, retrieval string) {
	t.Helper()
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recipient := clientcrypto.RecipientStringOf(identity)
	id := "req42"
	return BuildSubmissionLink("https://flick.example", id, submissionTokenFixture, clientcrypto.RecipientFingerprint(recipient), nil),
		BuildRetrievalLink("https://flick.example", id, retrievalTokenFixture, identity)
}

// Random tokens keep the exact 43-char base64url capability shape the fragments require.
var (
	submissionTokenFixture = randomToken()
	retrievalTokenFixture  = randomToken()
)

func TestParseRequestLinksRoundTrip(t *testing.T) {
	submission, retrieval := requestLinkPair(t)

	sub, err := ParseSubmissionLink(submission)
	if err != nil {
		t.Fatalf("parse submission: %v", err)
	}
	if sub.Origin != "https://flick.example" || sub.ID != "req42" || sub.Token != submissionTokenFixture || sub.MemoIdentity != nil {
		t.Errorf("submission = %+v", sub)
	}

	ret, err := ParseRetrievalLink(retrieval)
	if err != nil {
		t.Fatalf("parse retrieval: %v", err)
	}
	if ret.Origin != "https://flick.example" || ret.ID != "req42" || ret.Token != retrievalTokenFixture || ret.Identity == nil {
		t.Errorf("retrieval = %+v", ret)
	}
}

func TestParseSubmissionLinkCarriesTheMemoKey(t *testing.T) {
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	link := BuildSubmissionLink("https://flick.example", "req1", submissionTokenFixture,
		clientcrypto.RecipientFingerprint(clientcrypto.RecipientStringOf(identity)), identity)

	parsed, err := ParseSubmissionLink(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.MemoIdentity == nil || parsed.MemoIdentity.String() != identity.String() {
		t.Error("the memo key did not survive the round trip")
	}
}

func TestParseRequestLinksRejectMalformedInput(t *testing.T) {
	submission, retrieval := requestLinkPair(t)

	for name, value := range map[string]string{
		"empty":              "",
		"not a url":          "not-a-link",
		"query string":       submission + "?x=1",
		"wrong secret path":  strings.Replace(submission, "/r/", "/s/", 1),
		"no fragment":        "https://flick.example/r/req42",
		"secret fragment":    "https://flick.example/r/req42#key=AGE-SECRET-KEY-PQ-1-ABC",
		"submission swapped": retrieval,
	} {
		if _, err := ParseSubmissionLink(value); err == nil {
			t.Errorf("submission parse accepted %q (%s)", value, name)
		}
	}

	for name, value := range map[string]string{
		"empty":             "",
		"no receive prefix": "https://flick.example/r/req42#receive=" + retrievalTokenFixture,
		"retrieval swapped": submission,
		"query string":      retrieval + "?x=1",
	} {
		if _, err := ParseRetrievalLink(value); err == nil {
			t.Errorf("retrieval parse accepted %q (%s)", value, name)
		}
	}

	// A retrieval link without a key parses; the caller refuses to open with it.
	keyless, _, _ := strings.Cut(retrieval, "&key=")
	parsed, err := ParseRetrievalLink(keyless)
	if err != nil {
		t.Fatalf("keyless parse: %v", err)
	}
	if parsed.Identity != nil {
		t.Error("the keyless link claimed an identity")
	}
}

func TestParseRequestLinksKeepAPathPrefix(t *testing.T) {
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	link := BuildRetrievalLink("https://flick.example/flick", "req9", retrievalTokenFixture, identity)
	parsed, err := ParseRetrievalLink(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Origin != "https://flick.example/flick" {
		t.Errorf("origin = %q", parsed.Origin)
	}
}
