// Crockford base32 (no i, l, o, u) so a passphrase read aloud has no look-alikes.
const ALPHABET = '0123456789abcdefghjkmnpqrstvwxyz';
const GROUPS = 4;
const GROUP_LENGTH = 4;

// 80 bits: out of reach of an offline guess against the stored access-proof hash.
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
