import { describe, expect, it, vi } from 'vitest';
import { encodeKeyFragment } from '$lib/crypto/fragment';
import {
	type AccessVerifierPayload,
	type EncryptedFilePayload,
	type EncryptedTextPayload,
	KDF_ALGORITHM,
	KDF_ITERATIONS,
	KEY_LENGTH_BITS
} from '$lib/crypto/text';
import { createSecretApiClient, createShareUrl, SecretApiError } from './secrets';

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

const encryptedFilePayload: EncryptedFilePayload = {
	...encryptedPayload,
	encrypted_filename: '{"nonce":"filename-nonce","ciphertext":"filename-ciphertext"}',
	content_type: 'text/plain'
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
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

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
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

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

	it('creates file secrets without plaintext filename fields', async () => {
		expect.assertions(9);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ id: 'file-secret-id', expires_at: '2026-06-17T01:00:00Z' }), {
				status: 201,
				headers: { 'Content-Type': 'application/json' }
			})
		);
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(
			client.createFileSecret(encryptedFilePayload, 3600, accessVerifier)
		).resolves.toEqual({
			id: 'file-secret-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('http://api.local/api/secrets');
		expect(init?.method).toBe('POST');

		const body = JSON.parse(init?.body as string) as Record<string, unknown>;
		expect(body).toMatchObject({
			kind: 'file',
			ciphertext: encryptedFilePayload.ciphertext,
			nonce: encryptedFilePayload.nonce,
			encrypted_filename: encryptedFilePayload.encrypted_filename,
			content_type: 'text/plain',
			size_bytes: 16,
			ttl_seconds: 3600,
			max_views: 1,
			access: accessVerifier
		});
		expect(body).not.toHaveProperty('filename');
		expect(body).not.toHaveProperty('passphrase');
		expect(body).not.toHaveProperty('plaintext');
		expect(body).not.toHaveProperty('derived_key');
		expect(body).not.toHaveProperty('key');
	});

	it('creates ID-only share URLs', () => {
		expect.assertions(5);

		const url = createShareUrl('https://drop.example.com/app?x=1#frag', 'abc 123');

		expect(url).toBe('https://drop.example.com/s/abc%20123');
		expect(url).not.toContain('passphrase');
		expect(url).not.toContain('ciphertext');
		expect(url).not.toContain('nonce');
		expect(url).not.toContain('salt');
	});

	it('embeds a Model B key only in the fragment, never in path or query', () => {
		// 32-byte AES-256 key with edge-case bytes (base64url + / and padding).
		const key = new Uint8Array([
			1, 2, 3, 250, 255, 0, 127, 191, 254, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130,
			140, 150, 160, 170, 180, 190, 200, 210, 220, 230
		]);
		const url = createShareUrl('https://drop.example.com/app?x=1', 'sec_abc', key);

		// The decryption key must travel in the fragment only — the core Model B
		// invariant. A regression swapping url.hash for url.search would fail here.
		expect(url).toContain('#key=');
		const keyB64 = encodeKeyFragment(key).slice('key='.length);
		expect(url.split('#')[0]).not.toContain(keyB64);
	});

	it('maps API errors to client-safe messages', async () => {
		expect.assertions(8);

		const cases = [
			{
				code: 'payload_too_large',
				message: 'encrypted payload is too large',
				status: 413,
				expected: 'This file is too large.'
			},
			{
				code: 'invalid_access',
				message: 'access proof is invalid',
				status: 403,
				expected: 'Passphrase is invalid.'
			},
			{
				code: 'expired',
				message: 'secret has expired',
				status: 410,
				expected: 'This secret is no longer available.'
			},
			{
				code: 'invalid_json',
				message: 'request body does not match the create secret contract',
				status: 400,
				expected: 'Could not complete the request. Check the input and try again.'
			}
		];

		for (const testCase of cases) {
			const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
				new Response(
					JSON.stringify({
						error: {
							code: testCase.code,
							message: testCase.message
						}
					}),
					{ status: testCase.status, headers: { 'Content-Type': 'application/json' } }
				)
			);
			const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

			await expect(client.createTextSecret(encryptedPayload, 600, accessVerifier)).rejects.toThrow(
				testCase.expected
			);
			await expect(
				client.createTextSecret(encryptedPayload, 600, accessVerifier)
			).rejects.not.toThrow(testCase.message);
		}
	});

	it('uses a client-safe message for network failures', async () => {
		expect.assertions(3);

		const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error('Failed to fetch'));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(client.getSecretMetadata('secret-id')).rejects.toThrow(
			'Could not reach Flick. Check your connection and try again.'
		);
		await expect(client.getSecretMetadata('secret-id')).rejects.toBeInstanceOf(SecretApiError);
		await expect(client.getSecretMetadata('secret-id')).rejects.not.toThrow('Failed to fetch');
	});
});

// 'AAECAw==' decodes to bytes [0,1,2,3] — a real base64 payload so the S3 path
// can decode the ciphertext into a Blob for the multipart upload.
const largeFilePayload: EncryptedFilePayload = {
	...encryptedFilePayload,
	ciphertext: 'AAECAw==',
	size_bytes: 1000
};

describe('file secret routing', () => {
	it('takes the inline path at or below the inline threshold', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ id: 'inline-id', expires_at: '2026-06-17T01:00:00Z' }), {
				status: 201,
				headers: { 'Content-Type': 'application/json' }
			})
		);
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 1000, maxFileBytes: 100_000 }
		});

		await client.createFileSecret(largeFilePayload, 3600, accessVerifier);

		const [, init] = fetcher.mock.calls[0];
		const body = JSON.parse(init?.body as string) as Record<string, unknown>;
		expect(body).toHaveProperty('ciphertext', 'AAECAw==');
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it('takes the S3 path above the inline threshold', async () => {
		expect.assertions(7);

		const fetcher = vi
			.fn<typeof fetch>()
			// 1. stage the pending secret — no upload bytes yet.
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						id: 'large-id',
						expires_at: '2026-06-17T01:00:00Z',
						upload: {
							url: 'https://object-store.local/bucket',
							method: 'POST',
							expires_at: '2026-06-17T00:10:00Z',
							fields: { key: 'large-id', policy: 'signed-policy' },
							file_field: 'file'
						}
					}),
					{ status: 201, headers: { 'Content-Type': 'application/json' } }
				)
			)
			// 2. upload the ciphertext to the object store.
			.mockResolvedValueOnce(new Response(null, { status: 204 }))
			// 3. finalize.
			.mockResolvedValueOnce(
				new Response(JSON.stringify({ id: 'large-id', finalized: true }), {
					status: 200,
					headers: { 'Content-Type': 'application/json' }
				})
			);

		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		await expect(client.createFileSecret(largeFilePayload, 3600, accessVerifier)).resolves.toEqual({
			id: 'large-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		// Stage call: ciphertext omitted.
		const stageInit = fetcher.mock.calls[0][1];
		const stageBody = JSON.parse(stageInit?.body as string) as Record<string, unknown>;
		expect(stageBody).not.toHaveProperty('ciphertext');

		// Upload call: multipart to the object store with signed fields + file.
		const [uploadUrl, uploadInit] = fetcher.mock.calls[1];
		expect(uploadUrl).toBe('https://object-store.local/bucket');
		expect(uploadInit?.method).toBe('POST');
		expect(uploadInit?.body).toBeInstanceOf(FormData);
		expect((uploadInit?.body as FormData).get('file')).toBeInstanceOf(Blob);

		// Finalize call.
		expect(fetcher.mock.calls[2][0]).toBe('http://api.local/api/secrets/large-id/finalize');
	});

	it('rejects files above the absolute bound without a network call', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>();
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 1000, maxFileBytes: 500 }
		});

		await expect(client.createFileSecret(largeFilePayload, 3600)).rejects.toMatchObject({
			code: 'payload_too_large'
		});
		expect(fetcher).not.toHaveBeenCalled();
	});

	it('threads the abort signal to the upload and surfaces a cancel as an error, not a crash', async () => {
		expect.assertions(3);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						id: 'large-id',
						expires_at: '2026-06-17T01:00:00Z',
						upload: {
							url: 'https://object-store.local/bucket',
							method: 'POST',
							expires_at: '2026-06-17T00:10:00Z',
							fields: { key: 'large-id' },
							file_field: 'file'
						}
					}),
					{ status: 201, headers: { 'Content-Type': 'application/json' } }
				)
			)
			// The aborted upload leg rejects the way fetch() does on AbortSignal.
			.mockRejectedValueOnce(new DOMException('The operation was aborted.', 'AbortError'));

		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		const controller = new AbortController();
		// Aborted uploads must resolve to a SecretApiError, never leak the raw
		// DOMException as an unhandled crash.
		await expect(
			client.createFileSecret(largeFilePayload, 3600, undefined, controller.signal)
		).rejects.toMatchObject({ code: 'upload_cancelled' });

		const uploadInit = fetcher.mock.calls[1][1];
		expect(uploadInit?.signal).toBeInstanceOf(AbortSignal);
		expect(uploadInit?.signal).toBe(controller.signal);
	});

	it('surfaces an upload failure from the object store', async () => {
		expect.assertions(1);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(
				new Response(
					JSON.stringify({
						id: 'large-id',
						expires_at: '2026-06-17T01:00:00Z',
						upload: {
							url: 'https://object-store.local/bucket',
							method: 'POST',
							expires_at: '2026-06-17T00:10:00Z',
							fields: { key: 'large-id' },
							file_field: 'file'
						}
					}),
					{ status: 201, headers: { 'Content-Type': 'application/json' } }
				)
			)
			// Object store rejects (e.g. content-length-range exceeded).
			.mockResolvedValueOnce(new Response('<Error>EntityTooLarge</Error>', { status: 413 }));

		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		await expect(client.createFileSecret(largeFilePayload, 3600)).rejects.toMatchObject({
			code: 'upload_failed'
		});
	});
});
