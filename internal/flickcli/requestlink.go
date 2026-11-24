package flickcli

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Fragment shapes from docs/architecture/request-links.md, twins of
// web/src/lib/state/request-links.ts. Tokens travel only in Authorization
// headers, never in a request to the server.

const (
	requestPath        = "/r/"
	requestReceivePath = "/receive"
)

var (
	submissionFragment = regexp.MustCompile(`^#submit=([A-Za-z0-9_-]{43})&fp=([A-Za-z0-9_-]{43})(?:&mk=([A-Za-z0-9-]{1,128}))?$`)
	retrievalFragment  = regexp.MustCompile(`^#receive=([A-Za-z0-9_-]{43})(?:&key=([A-Za-z0-9-]{1,128}))?$`)
	managementToken    = regexp.MustCompile(`^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$`)
)

// MemoIdentity is nil when the requester attached no note.
type SubmissionLink struct {
	Origin       string
	ID           string
	Token        string
	Fingerprint  string
	MemoIdentity *age.HybridIdentity
}

// Identity is nil when the fragment carried no key: status and revoke still
// work, but the reply cannot be decrypted.
type RetrievalLink struct {
	Origin   string
	ID       string
	Token    string
	Identity *age.HybridIdentity
}

func ParseSubmissionLink(raw string) (SubmissionLink, error) {
	origin, id, fragment, err := parseRequestURL(raw, false)
	if err != nil {
		return SubmissionLink{}, err
	}
	match := submissionFragment.FindStringSubmatch(fragment)
	if match == nil || !managementToken.MatchString(match[1]) || !managementToken.MatchString(match[2]) {
		return SubmissionLink{}, errors.New("the link's fragment is not a Flick submission fragment (#submit=…&fp=…)")
	}
	link := SubmissionLink{Origin: origin, ID: id, Token: match[1], Fingerprint: match[2]}
	if match[3] != "" {
		identity, err := clientcrypto.ParseIdentity(match[3])
		if err != nil {
			return SubmissionLink{}, errors.New("the link's note key could not be read")
		}
		link.MemoIdentity = identity
	}
	return link, nil
}

func ParseRetrievalLink(raw string) (RetrievalLink, error) {
	origin, id, fragment, err := parseRequestURL(raw, true)
	if err != nil {
		return RetrievalLink{}, err
	}
	match := retrievalFragment.FindStringSubmatch(fragment)
	if match == nil || !managementToken.MatchString(match[1]) {
		return RetrievalLink{}, errors.New("the link's fragment is not a Flick retrieval fragment (#receive=…&key=…)")
	}
	link := RetrievalLink{Origin: origin, ID: id, Token: match[1]}
	if match[2] != "" {
		identity, err := clientcrypto.ParseIdentity(match[2])
		if err != nil {
			return RetrievalLink{}, errors.New("the link's key could not be read; without it the reply cannot be decrypted")
		}
		link.Identity = identity
	}
	return link, nil
}

func BuildSubmissionLink(origin, id, token, fingerprint string, memoIdentity *age.HybridIdentity) string {
	fragment := "#submit=" + token + "&fp=" + fingerprint
	if memoIdentity != nil {
		fragment += "&mk=" + memoIdentity.String()
	}
	return origin + requestPath + url.PathEscape(id) + fragment
}

func BuildRetrievalLink(origin, id, token string, identity *age.HybridIdentity) string {
	return origin + requestPath + url.PathEscape(id) + requestReceivePath + "#receive=" + token + "&key=" + identity.String()
}

// Query strings are rejected on every request endpoint, so they are rejected here too.
// The origin keeps any path prefix before /r/, like ParseShareLink does for /s/.
func parseRequestURL(raw string, wantReceive bool) (origin, id, fragment string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || !strings.Contains(raw, "://") {
		return "", "", "", errors.New("give a full request link (https://…/r/…)")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", "", "", errors.New("give a full request link (https://…/r/…)")
	}
	if parsed.RawQuery != "" {
		return "", "", "", errors.New("a request link carries no query string")
	}

	prefix, rest, found := strings.Cut(parsed.Path, requestPath)
	if !found || rest == "" {
		return "", "", "", fmt.Errorf("the link path does not contain a %s<id> request", requestPath)
	}
	if wantReceive {
		if rest, found = strings.CutSuffix(rest, requestReceivePath); !found {
			return "", "", "", errors.New("a retrieval link ends in /receive")
		}
	} else if strings.HasSuffix(rest, requestReceivePath) {
		return "", "", "", errors.New("this looks like a retrieval link; submit takes a submission link")
	}
	if rest == "" {
		return "", "", "", errors.New("the request ID is missing")
	}
	if strings.Contains(rest, "/") {
		return "", "", "", errors.New("the request path has extra segments after the ID")
	}
	if id, err = url.PathUnescape(rest); err != nil || id == "" || strings.ContainsAny(id, "/?#") {
		return "", "", "", errors.New("the request ID is not valid")
	}

	fragment = parsed.Fragment
	if parsed.RawFragment != "" {
		fragment = parsed.RawFragment
	}
	return strings.TrimRight(parsed.Scheme+"://"+parsed.Host+prefix, "/"), id, "#" + fragment, nil
}
