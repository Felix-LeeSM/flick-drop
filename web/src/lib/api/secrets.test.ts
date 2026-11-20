import { describe, expect, it, vi } from 'vitest';
import {
	type AccessVerifierPayload,
	KDF_ALGORITHM,
	KDF_ITERATIONS,
	KEY_LENGTH_BITS
} from '#lib/crypto/access.js';
import { generateSecretIdentity } from '#lib/crypto/age.js';
import type { SealedFile } from '#lib/crypto/secret.js';
import { en } from '#lib/i18n/messages/en/index.js';
import {
	createSecretApiClient,
	createShareUrl,
	type PassphraseEnvelope,
	SecretApiError
} from './secrets';

const accessVerifier: AccessVerifierPayload = {
	kdf: {
		algorithm: KDF_ALGORITHM,
		salt: 'access-salt-base64',
		iterations: KDF_ITERATIONS,
		key_length_bits: KEY_LENGTH_BITS
	},
	proof: 'proof-base64'
};

const textBody = new Uint8Array([10, 20, 30, 40, 50]);
const wrappedIdentity = new Uint8Array([7, 8, 9]);
const modelA: PassphraseEnvelope = { access: accessVerifier, wrappedIdentity };

// plaintextBytes drives routing; body.size is what the server is told and the bucket signs.
function sealedFile(plaintextBytes = 1000): SealedFile {
	return {
		body: new Blob([new Uint8Array([0, 1, 2, 3])]),
		encryptedFilename: new Uint8Array([0xff, 0xfe]),
		contentType: 'text/plain',
		plaintextBytes
	};
}

const created = (id: string) =>
	new Response(JSON.stringify({ id, expires_at: '2026-06-17T01:00:00Z' }), {
		status: 201,
		headers: { 'Content-Type': 'application/json' }
	});

// Fields that would carry a key, passphrase, plaintext, or the retired AES-GCM envelope.
const sensitiveFields = [
	'passphrase',
	'plaintext',
	'derived_key',
	'key',
	'identity',
	'filename',
	'nonce',
	'kdf'
];
const sensitiveIn = (body: Record<string, unknown>) =>
	sensitiveFields.filter((field) => field in body);

describe('secret API client', () => {
	it('creates Model B text secrets with only the age body and lifetime', async () => {
		expect.assertions(5);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(created('secret-id'));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(client.createTextSecret(textBody, 600)).resolves.toEqual({
			id: 'secret-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('http://api.local/api/secrets');
		expect(init?.method).toBe('POST');
		const body = JSON.parse(init?.body as string) as Record<string, unknown>;
		expect(body).toEqual({
			kind: 'text',
			ciphertext: 'ChQeKDI=',
			size_bytes: 5,
			ttl_seconds: 600,
			max_views: 1
		});
		expect(sensitiveIn(body)).toEqual([]);
	});

	it('sends the access verifier and wrapped identity for Model A', async () => {
		expect.assertions(3);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(created('secret-id'));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await client.createTextSecret(textBody, 600, modelA);

		const body = JSON.parse(fetcher.mock.calls[0][1]?.body as string) as Record<string, unknown>;
		expect(body).toMatchObject({ access: accessVerifier, wrapped_identity: 'BwgJ' });
		expect(sensitiveIn(body)).toEqual([]);
		expect(Object.keys(body).sort()).toEqual(
			[
				'access',
				'ciphertext',
				'kind',
				'max_views',
				'size_bytes',
				'ttl_seconds',
				'wrapped_identity'
			].sort()
		);
	});

	it('opens with an access proof and exposes the Flick-* headers and body stream', async () => {
		expect.assertions(9);

		const age = new Uint8Array([97, 103, 101, 1, 2, 3]);
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(
				Response.json({
					id: 'secret-id',
					kind: 'file',
					access: { kdf: accessVerifier.kdf },
					size_bytes: 6,
					expires_at: '2026-06-17T01:00:00Z'
				})
			)
			.mockResolvedValueOnce(
				new Response(age, {
					status: 200,
					headers: {
						'Content-Type': 'application/octet-stream',
						'Flick-Kind': 'file',
						'Flick-Content-Type': 'application/pdf',
						'Flick-Encrypted-Filename': 'AAECAw==',
						'Flick-Wrapped-Identity': 'BwgJ',
						'Flick-Expires-At': '2026-06-17T01:00:00Z'
					}
				})
			);
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(client.getSecretMetadata('secret-id')).resolves.toMatchObject({
			access: { kdf: accessVerifier.kdf }
		});
		const opened = await client.openSecret('secret-id', accessVerifier.proof);

		expect(opened).toMatchObject({
			kind: 'file',
			expiresAt: '2026-06-17T01:00:00Z',
			contentType: 'application/pdf',
			encryptedFilename: new Uint8Array([0, 1, 2, 3]),
			wrappedIdentity
		});
		expect(opened.body).toBeInstanceOf(ReadableStream);
		expect(new Uint8Array(await new Response(opened.body).arrayBuffer())).toEqual(age);

		expect(fetcher.mock.calls[0][0]).toBe('http://api.local/api/secrets/secret-id');
		expect(fetcher.mock.calls[1][0]).toBe('http://api.local/api/secrets/secret-id/open');
		expect(fetcher.mock.calls[1][1]?.method).toBe('POST');
		const body = JSON.parse(fetcher.mock.calls[1][1]?.body as string) as Record<string, unknown>;
		expect(body).toEqual({ access_proof: accessVerifier.proof });
		expect(body).not.toHaveProperty('passphrase');
	});

	it('opens a Model B text secret with an empty body and defaults missing metadata', async () => {
		expect.assertions(3);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(new Response(new Uint8Array([1]), { headers: { 'Flick-Kind': 'text' } }));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		const opened = await client.openSecret('secret-id');

		expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({});
		expect(opened).toMatchObject({
			kind: 'text',
			contentType: 'application/octet-stream',
			encryptedFilename: undefined,
			wrappedIdentity: undefined
		});
		expect(opened.body).toBeInstanceOf(ReadableStream);
	});

	it('rejects an open response without a known Flick-Kind', async () => {
		expect.assertions(1);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(new Response(new Uint8Array([1]), { headers: { 'Flick-Kind': 'zip' } }));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(client.openSecret('secret-id')).rejects.toMatchObject({
			code: 'invalid_response'
		});
	});

	it('creates file secrets without plaintext filename fields', async () => {
		expect.assertions(4);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(created('file-secret-id'));
		const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

		await expect(client.createFileSecret(sealedFile(), 3600, modelA)).resolves.toEqual({
			id: 'file-secret-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('http://api.local/api/secrets');
		const body = JSON.parse(init?.body as string) as Record<string, unknown>;
		expect(body).toEqual({
			kind: 'file',
			ciphertext: 'AAECAw==',
			encrypted_filename: '//4=',
			content_type: 'text/plain',
			size_bytes: 4,
			ttl_seconds: 3600,
			max_views: 1,
			access: accessVerifier,
			wrapped_identity: 'BwgJ'
		});
		expect(sensitiveIn(body)).toEqual([]);
	});

	it('creates ID-only share URLs', () => {
		expect.assertions(4);

		const url = createShareUrl('https://drop.example.com/app?x=1#frag', 'abc 123');

		expect(url).toBe('https://drop.example.com/s/abc%20123');
		expect(url).not.toContain('passphrase');
		expect(url).not.toContain('#');
		expect(url).not.toContain('x=1');
	});

	it('embeds a Model B identity only in the fragment, never in path or query', async () => {
		expect.assertions(3);

		const identity = await generateSecretIdentity();
		const url = createShareUrl('https://drop.example.com/app?x=1', 'sec_abc', identity);

		// The key must travel in the fragment only (Model B invariant).
		expect(new URL(url).hash).toBe(`#key=${identity}`);
		expect(url.split('#')[0]).toBe('https://drop.example.com/s/sec_abc');
		expect(url.split('#')[0]).not.toContain(identity);
	});

	it('maps API errors to client-safe messages', async () => {
		expect.assertions(14);

		const cases = [
			{
				code: 'payload_too_large',
				message: 'encrypted payload is too large',
				status: 413,
				expected: en.errors.fileTooLarge
			},
			{
				code: 'storage_full',
				message: 'object storage is at capacity; try again later',
				status: 503,
				expected: en.errors.storageFull
			},
			{
				code: 'invalid_access',
				message: 'access proof is invalid',
				status: 403,
				expected: en.errors.invalidPassphrase
			},
			{
				code: 'expired',
				message: 'secret has expired',
				status: 410,
				expected: en.errors.noLongerAvailable
			},
			{
				code: 'rate_limited',
				message: 'too many requests, slow down',
				status: 429,
				expected: en.errors.rateLimited
			},
			{
				code: 'proxy_limit',
				message: 'rate limited before the API',
				status: 429,
				expected: en.errors.rateLimited
			},
			{
				code: 'invalid_json',
				message: 'request body does not match the create secret contract',
				status: 400,
				expected: en.errors.requestFailed
			}
		];

		for (const testCase of cases) {
			const fetcher = vi
				.fn<typeof fetch>()
				.mockImplementation(async () =>
					Response.json(
						{ error: { code: testCase.code, message: testCase.message } },
						{ status: testCase.status }
					)
				);
			const client = createSecretApiClient({ baseUrl: 'http://api.local/', fetcher });

			await expect(client.createTextSecret(textBody, 600, modelA)).rejects.toThrow(
				testCase.expected
			);
			await expect(client.openSecret('secret-id')).rejects.not.toThrow(testCase.message);
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

const stagedLarge = (contentLength: string, extra: Record<string, string> = {}) =>
	new Response(
		JSON.stringify({
			id: 'large-id',
			expires_at: '2026-06-17T01:00:00Z',
			upload: {
				url: 'https://object-store.local/bucket/large-id?X-Amz-Signature=abc',
				method: 'PUT',
				expires_at: '2026-06-17T00:10:00Z',
				headers: { 'Content-Length': contentLength, ...extra }
			}
		}),
		{ status: 201, headers: { 'Content-Type': 'application/json' } }
	);

describe('file secret routing', () => {
	it('takes the inline path at or below the inline threshold', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(created('inline-id'));
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 1000, maxFileBytes: 100_000 }
		});

		await client.createFileSecret(sealedFile(1000), 3600, modelA);

		const body = JSON.parse(fetcher.mock.calls[0][1]?.body as string) as Record<string, unknown>;
		expect(body).toHaveProperty('ciphertext', 'AAECAw==');
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it('takes the S3 path above the inline threshold', async () => {
		expect.assertions(10);

		const fetcher = vi
			.fn<typeof fetch>()
			// Content-Length is dropped (scripts cannot set it); other signed headers travel.
			.mockResolvedValueOnce(stagedLarge('4', { 'x-amz-meta-probe': 'signed' }))
			.mockResolvedValueOnce(new Response(null, { status: 204 }))
			.mockResolvedValueOnce(Response.json({ id: 'large-id', finalized: true }));

		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		await expect(client.createFileSecret(sealedFile(1000), 3600, modelA)).resolves.toEqual({
			id: 'large-id',
			expires_at: '2026-06-17T01:00:00Z'
		});

		const stageBody = JSON.parse(fetcher.mock.calls[0][1]?.body as string) as Record<
			string,
			unknown
		>;
		expect(stageBody).not.toHaveProperty('ciphertext');
		expect(stageBody).toMatchObject({ size_bytes: 4, wrapped_identity: 'BwgJ' });

		const [uploadUrl, uploadInit] = fetcher.mock.calls[1];
		expect(uploadUrl).toBe('https://object-store.local/bucket/large-id?X-Amz-Signature=abc');
		expect(uploadInit?.method).toBe('PUT');
		expect(uploadInit?.body).toBeInstanceOf(Blob);
		expect(new Uint8Array(await (uploadInit?.body as Blob).arrayBuffer())).toEqual(
			new Uint8Array([0, 1, 2, 3])
		);
		expect(uploadInit?.headers).toEqual({ 'x-amz-meta-probe': 'signed' });

		expect(fetcher.mock.calls[2][0]).toBe('http://api.local/api/secrets/large-id/finalize');
		expect(fetcher).toHaveBeenCalledTimes(3);
	});

	// With S3 disabled the two limits arrive nearly equal; a payload exactly on the
	// ceiling must stay inline, since no S3 backend exists.
	it('keeps the inline path when the bound has been clamped to the threshold', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(created('inline-id'));
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 1016, maxFileBytes: 1000 }
		});

		await client.createFileSecret(sealedFile(1000), 3600);

		expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toHaveProperty(
			'ciphertext',
			'AAECAw=='
		);
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	// Content-Length is signed: catch a length drift locally instead of a bare bucket 403.
	it('refuses to upload when the signed length does not match the ciphertext', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(stagedLarge('999'));
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		await expect(client.createFileSecret(sealedFile(1000), 3600)).rejects.toMatchObject({
			code: 'upload_failed'
		});
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it('rejects files above the absolute bound without a network call', async () => {
		expect.assertions(2);

		const fetcher = vi.fn<typeof fetch>();
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 1000, maxFileBytes: 500 }
		});

		await expect(client.createFileSecret(sealedFile(1000), 3600)).rejects.toMatchObject({
			code: 'payload_too_large'
		});
		expect(fetcher).not.toHaveBeenCalled();
	});

	it('threads the abort signal to the upload and surfaces a cancel as an error, not a crash', async () => {
		expect.assertions(3);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(stagedLarge('4'))
			.mockRejectedValueOnce(new DOMException('The operation was aborted.', 'AbortError'));
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		const controller = new AbortController();
		await expect(
			client.createFileSecret(sealedFile(1000), 3600, undefined, controller.signal)
		).rejects.toMatchObject({ code: 'upload_cancelled' });

		const uploadInit = fetcher.mock.calls[1][1];
		expect(uploadInit?.signal).toBeInstanceOf(AbortSignal);
		expect(uploadInit?.signal).toBe(controller.signal);
	});

	it('surfaces an upload failure from the object store', async () => {
		expect.assertions(2);

		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(stagedLarge('4'))
			.mockResolvedValueOnce(new Response('<Error>EntityTooLarge</Error>', { status: 413 }));
		const client = createSecretApiClient({
			baseUrl: 'http://api.local/',
			fetcher,
			limits: { payloadInlineMaxBytes: 999, maxFileBytes: 100_000 }
		});

		await expect(client.createFileSecret(sealedFile(1000), 3600)).rejects.toMatchObject({
			code: 'upload_failed'
		});
		expect(fetcher).toHaveBeenCalledTimes(2);
	});
});
