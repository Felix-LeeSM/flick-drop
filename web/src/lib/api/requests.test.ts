import { beforeAll, describe, expect, it, vi } from 'vitest';
import { bytesToBase64 } from '#lib/crypto/encoding.js';
import {
	encryptRequestPayload,
	generateRequestKeys,
	type SealedRequest
} from '#lib/crypto/requests.js';
import { createRequestApiClient, prepareSubmission, requestLimits } from './requests';

const submitToken = 'A'.repeat(43);
const receiveToken = `${'B'.repeat(42)}A`;
const metadata = { public_key: '', fingerprint: '', expires_at: '2099-01-01T00:00:00Z' };
let identity = '';
let payload: SealedRequest;
let ciphertext = '';
beforeAll(async () => {
	const keys = await generateRequestKeys();
	identity = keys.identity;
	metadata.public_key = keys.public_key;
	metadata.fingerprint = keys.fingerprint;
	payload = await encryptRequestPayload(
		keys.public_key,
		{ kind: 'text', text: 'synthetic' },
		{ maxTextBytes: 1024, maxFileBytes: 1024 }
	);
	ciphertext = bytesToBase64(new Uint8Array(await payload.body.arrayBuffer()));
});

describe('request API capability and retry boundary', () => {
	it('creation sends only the public key and lifetime', async () => {
		const created = {
			id: 'fixture',
			expires_at: metadata.expires_at,
			submission_token: submitToken,
			retrieval_token: receiveToken
		};
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(created));
		expect(
			await createRequestApiClient({ baseUrl: '/', fetcher }).create(metadata.public_key, 3600)
		).toEqual(created);
		expect(fetcher.mock.calls[0][0]).toBe('/api/requests');
		expect(JSON.parse(String(fetcher.mock.calls[0][1]?.body))).toEqual({
			public_key: metadata.public_key,
			ttl_seconds: 3600
		});
		expect(fetcher.mock.calls[0][1]?.headers).toEqual({ 'Content-Type': 'application/json' });
	});
	it('status uses only the selected role bearer and never consumes a payload', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json({ ...metadata, generation: 1, can_submit: true }))
			.mockResolvedValueOnce(Response.json({ ...metadata, state: 'submitted' }));
		const api = createRequestApiClient({ baseUrl: 'https://api.example.test/', fetcher });
		await api.instructions('fixture', submitToken);
		await api.owner('fixture', receiveToken);
		for (const [index, token] of [submitToken, receiveToken].entries()) {
			const [url, init] = fetcher.mock.calls[index];
			expect(url).toBe(`https://api.example.test/api/requests/fixture${index ? '/owner' : ''}`);
			expect(init).toMatchObject({
				method: 'GET',
				credentials: 'omit',
				cache: 'no-store',
				redirect: 'error',
				referrerPolicy: 'no-referrer',
				headers: { authorization: `Bearer ${token}` }
			});
			expect(init?.body).toBeUndefined();
		}
	});
	it('open and revoke use empty HTTP bodies with retrieval authorization', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(
				new Response(payload.body, {
					headers: { 'Content-Type': 'application/octet-stream', 'Flick-Kind': 'text' }
				})
			)
			.mockResolvedValueOnce(Response.json({ state: 'cancelled' }));
		const api = createRequestApiClient({ baseUrl: '/', fetcher });
		const opened = await api.open('fixture', receiveToken);
		expect(opened.kind).toBe('text');
		expect(opened.encryptedFilename).toBeUndefined();
		expect(new Uint8Array(await new Response(opened.body).arrayBuffer())).toEqual(
			new Uint8Array(await payload.body.arrayBuffer())
		);
		await api.revoke('fixture', receiveToken);
		for (const [, init] of fetcher.mock.calls) {
			expect(init?.method).toBe('POST');
			expect(init?.body).toBeUndefined();
			expect(init?.headers).toEqual({ authorization: `Bearer ${receiveToken}` });
		}
	});
	it('reads the open kind and filename from headers and refuses malformed ones', async () => {
		const respond = (headers: Record<string, string>) =>
			createRequestApiClient({
				fetcher: vi.fn<typeof fetch>().mockResolvedValue(new Response('age', { headers }))
			}).open('fixture', receiveToken);
		const name = new Uint8Array([1, 2, 3]);
		await expect(
			respond({ 'Flick-Kind': 'file', 'Flick-Encrypted-Filename': bytesToBase64(name) })
		).resolves.toMatchObject({ kind: 'file', encryptedFilename: name });
		for (const headers of [
			{} as Record<string, string>,
			{ 'Flick-Kind': 'html' },
			{ 'Flick-Kind': 'file', 'Flick-Encrypted-Filename': 'AQID=' }
		]) {
			await expect(respond(headers)).rejects.toMatchObject({ code: 'invalid_response' });
		}
	});
	it('retains one immutable ciphertext and attempt token across response loss, status and explicit retry', async () => {
		const original = structuredClone(payload);
		const attempt = await prepareSubmission(original, 2);
		original.envelope.encrypted_filename = 'changed';
		const fetcher = vi
			.fn<typeof fetch>()
			.mockRejectedValueOnce(new Error('transport detail must not escape'))
			.mockResolvedValueOnce(Response.json({ generation: 2, state: 'waiting' }))
			.mockResolvedValueOnce(Response.json({ generation: 2, state: 'submitted' }));
		const api = createRequestApiClient({ fetcher });
		await expect(api.submit('fixture', submitToken, attempt)).rejects.toMatchObject({
			code: 'network_error',
			status: 0
		});
		expect(fetcher).toHaveBeenCalledTimes(1);
		expect(await api.attempt('fixture', submitToken, attempt)).toEqual({
			generation: 2,
			state: 'waiting'
		});
		await api.submit('fixture', submitToken, attempt);
		expect(fetcher.mock.calls[0][1]?.body).toBe(fetcher.mock.calls[2][1]?.body);
		expect(JSON.parse(attempt.body)).toEqual({
			generation: 2,
			attempt_token: attempt.attemptToken,
			kind: 'text',
			size_bytes: payload.size_bytes,
			envelope: { version: 2 },
			ciphertext
		});
		expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual({
			generation: 2,
			attempt_token: attempt.attemptToken
		});
		expect(Buffer.from(attempt.attemptToken, 'base64url')).toHaveLength(32);
		expect(Object.isFrozen(attempt)).toBe(true);
	});
	it.each([
		404, 409, 413, 429, 503
	])('keeps HTTP %i distinct without automatic retries or server error text', async (status) => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(Response.json({ error: { message: identity } }, { status }));
		const api = createRequestApiClient({ fetcher });
		const error = await api.open('fixture', receiveToken).catch((cause: unknown) => cause);
		expect(error).toMatchObject({ status });
		expect(String(error)).not.toContain(identity);
		expect(fetcher).toHaveBeenCalledTimes(1);
	});
	it('rejects receipt confusion, noncanonical tokens and malformed metadata', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(Response.json({ ...metadata, generation: 2, state: 'submitted' }));
		const api = createRequestApiClient({ fetcher });
		await expect(
			api.submit('fixture', submitToken, await prepareSubmission(payload, 1))
		).rejects.toMatchObject({ code: 'invalid_response' });
		await expect(api.owner('fixture', 'bad')).rejects.toMatchObject({ status: 404 });
		expect(fetcher).toHaveBeenCalledTimes(1);
		fetcher.mockResolvedValue(Response.json({ ...metadata, state: 'submitted' }));
		await expect(api.owner('fixture', receiveToken)).resolves.toMatchObject({ state: 'submitted' });
		fetcher.mockResolvedValue(Response.json({ ...metadata, expires_at: 'bad', state: 'waiting' }));
		await expect(api.owner('fixture', receiveToken)).rejects.toMatchObject({
			code: 'invalid_response'
		});
	});
	it('uses already-adjusted inline limits and the effective advertised file maximum', () => {
		expect(requestLimits({ payloadInlineMaxBytes: 1234, maxFileBytes: 100 })).toEqual({
			maxTextBytes: 1234,
			maxFileBytes: 100
		});
		expect(requestLimits({ payloadInlineMaxBytes: 1234, maxFileBytes: 5678 }).maxFileBytes).toBe(
			5678
		);
	});
});
