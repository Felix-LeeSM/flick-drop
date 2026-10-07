package flickcli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// SharePath is the web route a share link points at.
const SharePath = "/s/"

// ShareLink is a parsed share URL. Key is nil for Model A links, where the
// recipient supplies a passphrase instead.
type ShareLink struct {
	// Origin is the scheme://host the link was served from. In a deployed
	// Flick this is also the API base URL — the ingress serves /api and / on
	// the same host (deploy/base/ingress.yaml), and the web image is built with
	// PUBLIC_FLICK_API_BASE_URL=/. A split dev setup needs --url to override it.
	Origin string
	ID     string
	Key    []byte
}

// ParseShareLink accepts a full share URL or a bare secret ID. A bare ID has no
// origin, so the caller must supply one.
//
// The fragment key is parsed here and never sent anywhere: it is the Model B
// decryption key, and putting it in a request would hand the server the ability
// to decrypt the payload it stores.
func ParseShareLink(raw string) (ShareLink, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ShareLink{}, errors.New("a share link or secret ID is required")
	}

	// No scheme means a bare secret ID. Anything containing a slash at this
	// point is a malformed URL, not an ID, and saying so beats a confusing
	// "secret not found" after a pointless round trip.
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
		// url.Parse percent-decodes into Fragment; RawFragment holds the
		// original when they differ. The key is base64url, which never needs
		// escaping, so either form decodes — prefer the raw one.
		fragment := parsed.RawFragment
		if fragment == "" {
			fragment = parsed.Fragment
		}
		key, err := clientcrypto.DecodeKeyFragment(fragment)
		if err != nil {
			return ShareLink{}, err
		}
		link.Key = key
	}
	return link, nil
}

// BuildShareLink renders the URL to hand to a recipient. key is nil for Model A.
func BuildShareLink(origin, id string, key []byte) string {
	link := strings.TrimRight(origin, "/") + SharePath + url.PathEscape(id)
	if key != nil {
		link += "#" + clientcrypto.EncodeKeyFragment(key)
	}
	return link
}
