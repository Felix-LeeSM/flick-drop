// URL fragment transport for the Model B decryption key.
//
// The key lives only in the fragment (`#key=...`), which browsers never
// transmit to the server. Placing it in the path or query string would send it
// to the API and into access logs, letting the server decrypt the payload on
// open — see docs/architecture/security-model.md "Access Control Models".
//
// encodeKeyFragment returns the fragment body (`key=...`) without the leading
// `#`; the caller assigns it to URL.hash, which (re)adds the `#`. This keeps
// encode/decode symmetric — both operate on the same `key=...` form, so the
// sole caller no longer needs a compensating `.slice(1)`.

export const FRAGMENT_KEY_PREFIX = 'key=';

// A 256-bit key is 32 bytes -> 43 base64url chars (no padding). Allow headroom
// for larger keys; anything beyond is a spoofed fragment, not a real key, and
// must be rejected before decoding to bound allocation (DoS guard, #89).
const MAX_KEY_FRAGMENT_CHARS = 86;

export function encodeKeyFragment(raw: Uint8Array): string {
	return `${FRAGMENT_KEY_PREFIX}${encodeBase64Url(raw)}`;
}

export function decodeKeyFragment(hash: string): Uint8Array | null {
	// hash may include or omit the leading '#'.
	const stripped = hash.startsWith('#') ? hash.slice(1) : hash;
	if (!stripped.startsWith(FRAGMENT_KEY_PREFIX)) {
		return null;
	}
	const encoded = stripped.slice(FRAGMENT_KEY_PREFIX.length);
	if (encoded.length === 0 || encoded.length > MAX_KEY_FRAGMENT_CHARS) {
		return null;
	}
	try {
		return decodeBase64Url(encoded);
	} catch {
		return null;
	}
}

function encodeBase64Url(bytes: Uint8Array): string {
	let binary = '';
	const chunkSize = 0x80_00;
	for (let index = 0; index < bytes.length; index += chunkSize) {
		const chunk = bytes.subarray(index, index + chunkSize);
		binary += String.fromCharCode(...chunk);
	}
	return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function decodeBase64Url(value: string): Uint8Array {
	const padded = value.replace(/-/g, '+').replace(/_/g, '/');
	const binary = atob(padded);
	const bytes = new Uint8Array(binary.length);
	for (let index = 0; index < binary.length; index += 1) {
		bytes[index] = binary.charCodeAt(index);
	}
	return bytes;
}
