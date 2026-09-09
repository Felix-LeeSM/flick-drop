package clientcrypto

import (
	"encoding/base64"
	"errors"
	"strings"
)

// FragmentKeyPrefix marks the Model B key inside a share URL fragment.
// Mirrors web/src/lib/crypto/fragment.ts.
const FragmentKeyPrefix = "key="

// maxKeyFragmentChars bounds decoding before it allocates. A 256-bit key is 43
// base64url chars; anything past this ceiling is a spoofed fragment, not a key
// (DoS guard, #89).
const maxKeyFragmentChars = 86

// EncodeKeyFragment returns the fragment body without the leading '#', so the
// caller assigns it to URL.Fragment.
func EncodeKeyFragment(key []byte) string {
	return FragmentKeyPrefix + base64.RawURLEncoding.EncodeToString(key)
}

// DecodeKeyFragment reads the key back out of a share URL fragment. It accepts
// the fragment with or without its leading '#'.
func DecodeKeyFragment(fragment string) ([]byte, error) {
	stripped := strings.TrimPrefix(fragment, "#")
	encoded, found := strings.CutPrefix(stripped, FragmentKeyPrefix)
	if !found {
		return nil, errors.New("link fragment does not carry a key")
	}
	if encoded == "" || len(encoded) > maxKeyFragmentChars {
		return nil, errors.New("link fragment key has an implausible length")
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("link fragment key is not valid base64url")
	}
	return key, nil
}
