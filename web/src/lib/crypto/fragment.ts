// URL fragment transport for the Model B decryption key.
//
// The key lives only in the fragment (`#key=...`), which browsers never
// transmit to the server. Placing it in the path or query string would send it
// to the API and into access logs, letting the server decrypt the payload on
// open — see docs/architecture/security-model.md "Access Control Models".

export const FRAGMENT_KEY_PREFIX = 'key=';
export const FRAGMENT_PREFIX = `#${FRAGMENT_KEY_PREFIX}`;

export function encodeKeyFragment(raw: Uint8Array): string {
	return `${FRAGMENT_PREFIX}${encodeBase64Url(raw)}`;
}

export function decodeKeyFragment(hash: string): Uint8Array | null {
	// hash may include or omit the leading '#'.
	const stripped = hash.startsWith('#') ? hash.slice(1) : hash;
	if (!stripped.startsWith(FRAGMENT_KEY_PREFIX)) {
		return null;
	}
	const encoded = stripped.slice(FRAGMENT_KEY_PREFIX.length);
	if (encoded.length === 0) {
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
