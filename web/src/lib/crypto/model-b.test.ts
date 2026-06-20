import { describe, expect, it } from 'vitest';

import { decryptTextWithKey, encryptTextWithKey, generateSecretKey } from './text';

describe('model B raw key encryption', () => {
	it('round trips text with a generated key', async () => {
		const { key } = await generateSecretKey();
		const encrypted = await encryptTextWithKey('link bearer secret', key);

		expect(encrypted.ciphertext).not.toContain('link bearer secret');
		expect(encrypted).not.toHaveProperty('passphrase');
		expect(encrypted).not.toHaveProperty('plaintext');

		const decrypted = await decryptTextWithKey(encrypted, key);
		expect(decrypted).toBe('link bearer secret');
	});

	it('generates distinct random keys', async () => {
		const a = await generateSecretKey();
		const b = await generateSecretKey();
		expect(a.raw).not.toEqual(b.raw);
		expect(a.raw.length).toBe(32);
	});
});
