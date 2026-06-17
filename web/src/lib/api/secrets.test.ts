import { describe, expect, it, vi } from 'vitest';

import {
	KDF_ALGORITHM,
	KDF_ITERATIONS,
	KEY_LENGTH_BITS,
	type AccessVerifierPayload,
	type EncryptedTextPayload
} from '$lib/crypto/text';
import { createSecretAPIClient, createShareURL } from './secrets';

const encryptedPayload: EncryptedTextPayload = {
	ciphertext: 'ciphertext-base64',
	nonce: 'nonce-base64',
	kdf: {
		algorithm: KDF_ALGORITHM,
		salt: 'salt-base64',
		iterations: KDF_ITERATIONS,
		key_length_bits: KEY_LENGTH_BITS
	},
	size_bytes: 16
};

const accessVerifier: AccessVerifierPayload = {
	kdf: {
		algorithm: KDF_ALGORITHM,
		salt: 'access-salt-base64',
		iterations: KDF_ITERATIONS,
		key_length_bits: KEY_LENGTH_BITS
	},
	proof: 'proof-base64'
};

describe('secret API client', () => {
	it('creates text secrets without sensitive fields', async () => {
		expect.assertions(8);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ id: 'secret-id', expires_at: '2026-06-17T01:00:00Z' }), {
				status: 201,
				headers: { 'Content-Type': 'application/json' }
			})
		);
		const client = createSecretAPIClient({ baseURL: 'http://api.local/', fetcher });

		await expect(client.createTextSecret(encryptedPayload, 600, accessVerifier)).resolves.toEqual({
			id: 'secret-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('http://api.local/api/secrets');
		expect(init?.method).toBe('POST');

		const body = JSON.parse(init?.body as string) as Record<string, unknown>;
		expect(body).toMatchObject({
			kind: 'text',
			ciphertext: 'ciphertext-base64',
			nonce: 'nonce-base64',
			size_bytes: 16,
			ttl_seconds: 600,
			max_views: 1,
			access: accessVerifier
		});
		expect(body).not.toHaveProperty('passphrase');
		expect(body).not.toHaveProperty('plaintext');
		expect(body).not.toHaveProperty('derived_key');
		expect(body).not.toHaveProperty('key');
	});

	it('opens secrets with an access proof instead of a passphrase', async () => {
		expect.assertions(8);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						id: 'secret-id',
						kind: 'text',
						access: { kdf: accessVerifier.kdf },
						size_bytes: 16,
						expires_at: '2026-06-17T01:00:00Z'
					}),
					{ status: 200, headers: { 'Content-Type': 'application/json' } }
				)
			)
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						id: 'secret-id',
						kind: 'text',
						...encryptedPayload,
						expires_at: '2026-06-17T01:00:00Z'
					}),
					{ status: 200, headers: { 'Content-Type': 'application/json' } }
				)
			);
		const client = createSecretAPIClient({ baseURL: 'http://api.local/', fetcher });

		await expect(client.getSecretMetadata('secret-id')).resolves.toMatchObject({
			access: { kdf: accessVerifier.kdf }
		});
		await expect(client.openSecret('secret-id', accessVerifier.proof)).resolves.toMatchObject({
			ciphertext: encryptedPayload.ciphertext
		});

		expect(fetcher.mock.calls[0][0]).toBe('http://api.local/api/secrets/secret-id');
		expect(fetcher.mock.calls[1][0]).toBe('http://api.local/api/secrets/secret-id/open');
		expect(fetcher.mock.calls[1][1]?.method).toBe('POST');
		const body = JSON.parse(fetcher.mock.calls[1][1]?.body as string) as Record<string, unknown>;
		expect(body).toEqual({ access_proof: accessVerifier.proof });
		expect(body).not.toHaveProperty('passphrase');
		expect(body).not.toHaveProperty('derived_key');
	});

	it('creates ID-only share URLs', () => {
		expect.assertions(5);

		const url = createShareURL('https://drop.example.com/app?x=1#frag', 'abc 123');

		expect(url).toBe('https://drop.example.com/s/abc%20123');
		expect(url).not.toContain('passphrase');
		expect(url).not.toContain('ciphertext');
		expect(url).not.toContain('nonce');
		expect(url).not.toContain('salt');
	});
});
