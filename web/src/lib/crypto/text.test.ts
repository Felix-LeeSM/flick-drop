import { describe, expect, it } from 'vitest';

import {
	KDF_ALGORITHM,
	KDF_ITERATIONS,
	KEY_LENGTH_BITS,
	decryptText,
	encryptText
} from './text';

describe('text encryption', () => {
	it('round trips text with browser-compatible metadata', async () => {
		expect.assertions(8);

		const encrypted = await encryptText('temporary secret', 'correct passphrase', {
			salt: new Uint8Array(16).fill(1),
			nonce: new Uint8Array(12).fill(2)
		});

		expect(encrypted.ciphertext).not.toContain('temporary secret');
		expect(encrypted).not.toHaveProperty('passphrase');
		expect(encrypted).not.toHaveProperty('plaintext');
		expect(encrypted.kdf.algorithm).toBe(KDF_ALGORITHM);
		expect(encrypted.kdf.iterations).toBe(KDF_ITERATIONS);
		expect(encrypted.kdf.key_length_bits).toBe(KEY_LENGTH_BITS);
		expect(encrypted.size_bytes).toBe(16);
		await expect(decryptText(encrypted, 'correct passphrase')).resolves.toBe('temporary secret');
	});

	it('fails with the wrong passphrase', async () => {
		expect.assertions(1);

		const encrypted = await encryptText('temporary secret', 'correct passphrase', {
			salt: new Uint8Array(16).fill(3),
			nonce: new Uint8Array(12).fill(4)
		});

		await expect(decryptText(encrypted, 'wrong passphrase')).rejects.toThrow();
	});

	it('rejects weak KDF parameters', async () => {
		expect.assertions(1);

		await expect(
			encryptText('temporary secret', 'correct passphrase', {
				salt: new Uint8Array(16).fill(5),
				nonce: new Uint8Array(12).fill(6),
				iterations: KDF_ITERATIONS - 1
			})
		).rejects.toThrow('PBKDF2 iterations are below the minimum');
	});
});
