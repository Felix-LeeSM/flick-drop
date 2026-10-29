package clientcrypto

import (
	"errors"
	"regexp"
	"strings"

	"filippo.io/age"
)

// FragmentKeyPrefix marks the Model B identity inside a share URL fragment
// (`#key=AGE-SECRET-KEY-PQ-1...`). Mirrors web/src/lib/crypto/fragment.ts.
const FragmentKeyPrefix = "key="

// maxKeyFragmentChars bounds parsing. A hybrid identity is 77 characters;
// anything much longer is a spoofed fragment, not a key (DoS guard, #89).
const maxKeyFragmentChars = 128

// identityPattern is uppercase bech32 after the prefix, as in the browser.
var identityPattern = regexp.MustCompile(`^` + IdentityPrefix + `[02-9AC-HJ-NP-Z]+$`)

// EncodeKeyFragment returns the fragment body without the leading '#', so the
// caller assigns it to URL.Fragment.
func EncodeKeyFragment(id *age.HybridIdentity) string {
	return FragmentKeyPrefix + id.String()
}

// DecodeKeyFragment reads the identity back out of a share URL fragment. It
// accepts the fragment with or without its leading '#'.
func DecodeKeyFragment(fragment string) (*age.HybridIdentity, error) {
	stripped := strings.TrimPrefix(fragment, "#")
	encoded, found := strings.CutPrefix(stripped, FragmentKeyPrefix)
	if !found {
		return nil, errors.New("link fragment does not carry a key")
	}
	if encoded == "" || len(encoded) > maxKeyFragmentChars {
		return nil, errors.New("link fragment key has an implausible length")
	}
	if !identityPattern.MatchString(encoded) {
		return nil, errors.New("link fragment key is not an age identity")
	}
	id, err := ParseIdentity(encoded)
	if err != nil {
		return nil, errors.New("link fragment key is not a valid age identity")
	}
	return id, nil
}
