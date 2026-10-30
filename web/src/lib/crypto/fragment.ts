// URL fragment transport for the Model B identity (contracts/age-format-v1.md).
//
// The identity lives only in the fragment (`#key=AGE-SECRET-KEY-PQ-1...`),
// which browsers never transmit to the server. Placing it in the path or query
// string would send it to the API and into access logs, letting the server
// decrypt the payload on open — see docs/architecture/security-model.md
// "Access Control Models".
//
// encodeKeyFragment returns the fragment body (`key=...`) without the leading
// `#`; the caller assigns it to URL.hash, which (re)adds the `#`.

import { IDENTITY_PREFIX, parseSecretIdentity } from '#lib/crypto/age.js';

export const FRAGMENT_KEY_PREFIX = 'key=';

// A hybrid identity is 77 characters. Anything much longer is a spoofed
// fragment and is rejected before parsing to bound work (DoS guard, #89).
const MAX_KEY_FRAGMENT_CHARS = 128;
const IDENTITY_PATTERN = new RegExp(`^${IDENTITY_PREFIX}[02-9AC-HJ-NP-Z]+$`);

export function encodeKeyFragment(identity: string): string {
	return `${FRAGMENT_KEY_PREFIX}${parseSecretIdentity(identity)}`;
}

export function decodeKeyFragment(hash: string): string | null {
	// hash may include or omit the leading '#'.
	const stripped = hash.startsWith('#') ? hash.slice(1) : hash;
	if (!stripped.startsWith(FRAGMENT_KEY_PREFIX)) {
		return null;
	}
	const encoded = stripped.slice(FRAGMENT_KEY_PREFIX.length);
	if (encoded.length === 0 || encoded.length > MAX_KEY_FRAGMENT_CHARS) {
		return null;
	}
	// Uppercase bech32 only; the age library checks the checksum on decrypt.
	if (!IDENTITY_PATTERN.test(encoded)) {
		return null;
	}
	return encoded;
}
