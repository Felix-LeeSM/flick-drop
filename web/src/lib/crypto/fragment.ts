// The Model B identity lives only in the URL fragment, which browsers never send; path or
// query would let the server decrypt on open (docs/architecture/security-model.md).

import { IDENTITY_PREFIX, parseSecretIdentity } from '#lib/crypto/age.js';

export const FRAGMENT_KEY_PREFIX = 'key=';

// A hybrid identity is 77 characters; reject much longer input before parsing (DoS guard).
const MAX_KEY_FRAGMENT_CHARS = 128;
const IDENTITY_PATTERN = new RegExp(`^${IDENTITY_PREFIX}[02-9AC-HJ-NP-Z]+$`);

export function encodeKeyFragment(identity: string): string {
	return `${FRAGMENT_KEY_PREFIX}${parseSecretIdentity(identity)}`;
}

export function decodeKeyFragment(hash: string): string | null {
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
