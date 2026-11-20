package clientcrypto

import (
	"errors"
	"regexp"
	"strings"

	"filippo.io/age"
)

// Mirrors web/src/lib/crypto/fragment.ts.
const FragmentKeyPrefix = "key="

// A hybrid identity is 77 characters; longer is a spoofed fragment (DoS guard).
const maxKeyFragmentChars = 128

var identityPattern = regexp.MustCompile(`^` + IdentityPrefix + `[02-9AC-HJ-NP-Z]+$`)

func EncodeKeyFragment(id *age.HybridIdentity) string {
	return FragmentKeyPrefix + id.String()
}

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
