// Crockford's base32 alphabet in lowercase: 32 symbols without i, l, o, or u, so a passphrase
// read aloud or retyped has no look-alike characters.
const ALPHABET = '0123456789abcdefghjkmnpqrstvwxyz';
const GROUPS = 4;
const GROUP_LENGTH = 4;

/**
 * Generates a passphrase for the sender to share separately, such as `k3p9-x2m4-7bq0-hv8d`.
 * 16 symbols of 5 bits carry 80 bits from `crypto.getRandomValues`, out of reach of an offline
 * guess against the stored access-proof hash. The value lives only in page memory like a typed
 * passphrase.
 */
export function generatePassphrase(): string {
	const bytes = crypto.getRandomValues(new Uint8Array(GROUPS * GROUP_LENGTH));
	// 32 divides 256, so the low five bits of a uniform byte pick a symbol without bias.
	const symbols = Array.from(bytes, (byte) => ALPHABET[byte & 31]).join('');
	const groups: string[] = [];
	for (let start = 0; start < symbols.length; start += GROUP_LENGTH) {
		groups.push(symbols.slice(start, start + GROUP_LENGTH));
	}
	return groups.join('-');
}
