package flickcli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

const SharePath = "/s/"

// Identity is nil for Model A links.
type ShareLink struct {
	// Also the API base URL when deployed (deploy/base/ingress.yaml); a split dev setup needs --url.
	Origin   string
	ID       string
	Identity *age.HybridIdentity
}

// The fragment identity is the Model B key; never put it in a request.
func ParseShareLink(raw string) (ShareLink, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ShareLink{}, errors.New("a share link or secret ID is required")
	}

	// A slash here means a malformed URL, not an ID; fail before a pointless round trip.
	if !strings.Contains(trimmed, "://") {
		if strings.ContainsAny(trimmed, "/#?") {
			return ShareLink{}, fmt.Errorf("%q is neither a full share link nor a secret ID", raw)
		}
		return ShareLink{ID: trimmed}, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ShareLink{}, fmt.Errorf("could not parse the share link: %w", err)
	}
	if parsed.Host == "" {
		return ShareLink{}, fmt.Errorf("share link %q has no host", raw)
	}

	prefix, id, found := strings.Cut(parsed.Path, SharePath)
	if !found || id == "" {
		return ShareLink{}, fmt.Errorf("share link %q does not contain a %s<id> path", raw, SharePath)
	}
	if strings.Contains(id, "/") {
		return ShareLink{}, fmt.Errorf("share link %q has extra path segments after the secret ID", raw)
	}
	id, err = url.PathUnescape(id)
	if err != nil {
		return ShareLink{}, fmt.Errorf("secret ID in %q is not valid: %w", raw, err)
	}

	link := ShareLink{
		Origin: strings.TrimRight(parsed.Scheme+"://"+parsed.Host+prefix, "/"),
		ID:     id,
	}
	if parsed.Fragment != "" {
		// The identity is uppercase bech32, so Fragment and RawFragment both parse; prefer the raw one.
		fragment := parsed.RawFragment
		if fragment == "" {
			fragment = parsed.Fragment
		}
		identity, err := clientcrypto.DecodeKeyFragment(fragment)
		if err != nil {
			return ShareLink{}, err
		}
		link.Identity = identity
	}
	return link, nil
}

func BuildShareLink(origin, id string, identity *age.HybridIdentity) string {
	link := strings.TrimRight(origin, "/") + SharePath + url.PathEscape(id)
	if identity != nil {
		link += "#" + clientcrypto.EncodeKeyFragment(identity)
	}
	return link
}
