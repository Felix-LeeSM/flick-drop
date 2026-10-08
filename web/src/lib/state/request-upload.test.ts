// biome-ignore-all lint/style/useNamingConvention: synthetic fixtures follow request wire names.
import { createHash } from 'node:crypto';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	createRequestApiClient,
	prepareLargeSubmission,
	type ReservedUpload
} from '$lib/api/requests';
import type { RequestPayload } from '$lib/crypto/requests';
import vectors from '../../../../tests/fixtures/request-crypto-v1.json';
import { cancelRequestUpload, finishRequestUpload, inspectRequestAttempt } from './request-upload';

const token = 'A'.repeat(43);
const deadline = '2099-01-01T01:00:00Z';
const reserved: ReservedUpload = {
	generation: 1,
	state: 'uploading',
	reservation_expires_at: '2099-01-01T00:15:00Z',
	upload: {
		url: 'https://storage.example.test/synthetic',
		method: 'PUT',
		expires_at: '2099-01-01T00:15:00Z',
		headers: { 'Content-Length': String(vectors.file.payload.size_bytes + 16) }
	}
};
const instructions = {
	public_key: vectors.public_key,
	fingerprint: vectors.fingerprint,
	expires_at: deadline,
	generation: 2,
	can_submit: true
};
class UploadRequest {
	status = 200;
	upload = { onprogress: null as ((event: Partial<ProgressEvent>) => void) | null };
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onabort: (() => void) | null = null;
	ontimeout: (() => void) | null = null;
	open = vi.fn();
	setRequestHeader = vi.fn();
	send = vi.fn();
	abort = vi.fn();
}
function transport() {
	const xhr = new UploadRequest();
	vi.stubGlobal(
		'XMLHttpRequest',
		class {
			constructor() {
				// biome-ignore lint/correctness/noConstructorReturn: native transport double for awaited tests.
				return xhr;
			}
		}
	);
	return xhr;
}
const attempt = () =>
	prepareLargeSubmission(structuredClone(vectors.file.payload) as RequestPayload, 1);
afterEach(() => vi.unstubAllGlobals());

describe('request upload immutable attempt and finalization', () => {
	it('snapshots encrypted metadata and SHA-256, keeping payload bytes out of reservation HTTP', async () => {
		const payload = structuredClone(vectors.file.payload) as RequestPayload;
		const prepared = await prepareLargeSubmission(payload, 1);
		payload.envelope.nonce = 'mutated';
		payload.ciphertext = 'mutated';
		const body = JSON.parse(prepared.body);
		expect(body.ciphertext).toBeUndefined();
		expect(body.size_bytes).toBe(vectors.file.payload.size_bytes);
		expect(body.ciphertext_sha256).toBe(
			createHash('sha256')
				.update(Buffer.from(vectors.file.payload.ciphertext, 'base64'))
				.digest('base64')
		);
		expect(body.envelope).toEqual(vectors.file.payload.envelope);
		expect(prepared.ciphertext).toBe(vectors.file.payload.ciphertext);
		expect(Buffer.from(prepared.attemptToken, 'base64url')).toHaveLength(32);
		expect(Object.isFrozen(prepared)).toBe(true);
	});
	it('100% PUT progress is not accepted until explicit finalize confirms, and object storage receives no capability', async () => {
		const xhr = transport();
		const finalized = Promise.withResolvers<Response>();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json(reserved))
			.mockImplementationOnce(() => finalized.promise);
		const api = createRequestApiClient({ fetcher });
		const prepared = await attempt();
		const progress = vi.fn();
		let accepted = false;
		const result = finishRequestUpload(
			api,
			'fixture',
			token,
			prepared,
			deadline,
			new AbortController().signal,
			progress,
			vi.fn()
		).then(() => {
			accepted = true;
		});
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		expect(new Uint8Array(xhr.send.mock.calls[0][0])).toEqual(
			new Uint8Array(Buffer.from(prepared.ciphertext as string, 'base64'))
		);
		expect(xhr.setRequestHeader).not.toHaveBeenCalled();
		xhr.upload.onprogress?.({ loaded: 44, total: 44, lengthComputable: true });
		expect(progress).toHaveBeenLastCalledWith({ stage: 'uploading', loaded: 44, total: 44 });
		expect(accepted).toBe(false);
		expect(fetcher).toHaveBeenCalledTimes(1);
		xhr.onload?.();
		await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
		expect(progress).toHaveBeenLastCalledWith({ stage: 'finalizing' });
		expect(accepted).toBe(false);
		expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual({
			generation: 1,
			attempt_token: prepared.attemptToken
		});
		finalized.resolve(Response.json({ generation: 1, state: 'submitted' }));
		await result;
		expect(accepted).toBe(true);
	});
	it('abort prevents finalize and late XHR callbacks; it does not claim server abandonment', async () => {
		const xhr = transport();
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(reserved));
		const controller = new AbortController();
		const api = createRequestApiClient({ fetcher });
		const result = finishRequestUpload(
			api,
			'fixture',
			token,
			await attempt(),
			deadline,
			controller.signal,
			vi.fn(),
			vi.fn()
		);
		const rejected = expect(result).rejects.toMatchObject({ code: 'upload_cancelled' });
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		const late = xhr.onload;
		controller.abort();
		late?.();
		await rejected;
		expect(xhr.abort).toHaveBeenCalled();
		expect(fetcher).toHaveBeenCalledTimes(1);
	});
	it('unknown reservation can recover only the exact same body and ciphertext', async () => {
		const xhr = transport();
		const prepared = await attempt();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockRejectedValueOnce(new Error('lost response'))
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'waiting' }))
			.mockResolvedValueOnce(Response.json(reserved))
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'submitted' }));
		const api = createRequestApiClient({ fetcher });
		const signal = new AbortController().signal;
		await expect(
			finishRequestUpload(api, 'fixture', token, prepared, deadline, signal, vi.fn(), vi.fn())
		).rejects.toMatchObject({ code: 'network_error' });
		expect(fetcher).toHaveBeenCalledTimes(1);
		expect(xhr.send).not.toHaveBeenCalled();
		expect(await inspectRequestAttempt(api, 'fixture', token, prepared, signal)).toMatchObject({
			state: 'waiting'
		});
		const retry = finishRequestUpload(
			api,
			'fixture',
			token,
			prepared,
			deadline,
			signal,
			vi.fn(),
			vi.fn()
		);
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		xhr.onload?.();
		await retry;
		expect(fetcher.mock.calls[0][1]?.body).toBe(fetcher.mock.calls[2][1]?.body);
	});
	it('lost finalize confirmation is recovered through an accepted receipt, without another PUT or finalize', async () => {
		const xhr = transport();
		const prepared = await attempt();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json(reserved))
			.mockRejectedValueOnce(new Error('lost finalization'))
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'accepted' }));
		const api = createRequestApiClient({ fetcher });
		const signal = new AbortController().signal;
		const result = finishRequestUpload(
			api,
			'fixture',
			token,
			prepared,
			deadline,
			signal,
			vi.fn(),
			vi.fn()
		);
		const failed = expect(result).rejects.toMatchObject({ code: 'network_error' });
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		xhr.onload?.();
		await failed;
		expect(await inspectRequestAttempt(api, 'fixture', token, prepared, signal)).toMatchObject({
			state: 'accepted'
		});
		expect(xhr.send).toHaveBeenCalledOnce();
		expect(fetcher).toHaveBeenCalledTimes(3);
	});
	it('an unknown PUT retries only the same encrypted bytes after an uploading receipt', async () => {
		const xhr = transport();
		const prepared = await attempt();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json(reserved))
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'uploading' }))
			.mockResolvedValueOnce(Response.json(reserved))
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'submitted' }));
		const api = createRequestApiClient({ fetcher });
		const signal = new AbortController().signal;
		const first = finishRequestUpload(
			api,
			'fixture',
			token,
			prepared,
			deadline,
			signal,
			vi.fn(),
			vi.fn()
		);
		const failed = expect(first).rejects.toMatchObject({ code: 'network_error' });
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		xhr.onerror?.();
		await failed;
		expect(await inspectRequestAttempt(api, 'fixture', token, prepared, signal)).toMatchObject({
			state: 'uploading'
		});
		const retry = finishRequestUpload(
			api,
			'fixture',
			token,
			prepared,
			deadline,
			signal,
			vi.fn(),
			vi.fn()
		);
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledTimes(2));
		xhr.onload?.();
		await retry;
		expect(fetcher.mock.calls[0][1]?.body).toBe(fetcher.mock.calls[2][1]?.body);
		expect(new Uint8Array(xhr.send.mock.calls[0][0])).toEqual(
			new Uint8Array(xhr.send.mock.calls[1][0])
		);
	});
	it('a recovered accepted reservation never PUTs or finalizes again', async () => {
		const xhr = transport();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(Response.json({ generation: 1, state: 'submitted' }));
		await finishRequestUpload(
			createRequestApiClient({ fetcher }),
			'fixture',
			token,
			await attempt(),
			deadline,
			new AbortController().signal,
			vi.fn(),
			vi.fn()
		);
		expect(xhr.send).not.toHaveBeenCalled();
		expect(fetcher).toHaveBeenCalledOnce();
	});
	it('an expired upload instruction cannot start PUT; a reservation cannot extend the request deadline', async () => {
		const xhr = transport();
		const prepared = await attempt();
		const stale = {
			...reserved,
			upload: { ...reserved.upload, expires_at: '2000-01-01T00:00:00Z' }
		};
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json(stale))
			.mockResolvedValueOnce(Response.json(reserved));
		const api = createRequestApiClient({ fetcher });
		const signal = new AbortController().signal;
		await expect(
			finishRequestUpload(api, 'fixture', token, prepared, deadline, signal, vi.fn(), vi.fn())
		).rejects.toMatchObject({ code: 'upload_expired' });
		await expect(
			api.reserve('fixture', token, prepared, '2000-01-01T00:00:00Z')
		).rejects.toMatchObject({ code: 'invalid_response' });
		expect(xhr.send).not.toHaveBeenCalled();
	});
});

describe('request upload cancellation and generation recovery', () => {
	it('linearizes an unknown waiting reservation before abandoning, then requires a newer generation', async () => {
		const prepared = await attempt();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'waiting' }))
			.mockResolvedValueOnce(Response.json(reserved))
			.mockResolvedValueOnce(Response.json({ generation: 2, state: 'waiting' }))
			.mockResolvedValueOnce(Response.json(instructions));
		const api = createRequestApiClient({ baseUrl: '/', fetcher });
		expect(
			await cancelRequestUpload(
				api,
				'fixture',
				token,
				prepared,
				deadline,
				new AbortController().signal
			)
		).toEqual({ state: 'renewed', instructions });
		expect(fetcher.mock.calls.map(([url]) => url)).toEqual([
			'/api/requests/fixture/attempt',
			'/api/requests/fixture/upload',
			'/api/requests/fixture/abandon',
			'/api/requests/fixture'
		]);
		expect(fetcher.mock.calls[1][1]?.body).toBe(prepared.body);
	});
	it('recovers unknown abandon through old-generation rejection and fresh instructions; no automatic new attempt', async () => {
		const prepared = await attempt();
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValueOnce(Response.json({ generation: 1, state: 'uploading' }))
			.mockRejectedValueOnce(new Error('abandon committed but response lost'))
			.mockResolvedValueOnce(Response.json({}, { status: 404 }))
			.mockResolvedValueOnce(Response.json(instructions));
		const api = createRequestApiClient({ fetcher });
		const signal = new AbortController().signal;
		await expect(
			cancelRequestUpload(api, 'fixture', token, prepared, deadline, signal)
		).rejects.toMatchObject({ code: 'network_error' });
		expect(await inspectRequestAttempt(api, 'fixture', token, prepared, signal)).toEqual({
			state: 'renewed',
			instructions
		});
		expect(fetcher).toHaveBeenCalledTimes(4);
	});
	it('a finalize winner remains accepted and cannot be abandoned by the submitter', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(Response.json({ generation: 1, state: 'accepted' }));
		const api = createRequestApiClient({ fetcher });
		expect(
			await cancelRequestUpload(
				api,
				'fixture',
				token,
				await attempt(),
				deadline,
				new AbortController().signal
			)
		).toEqual({ generation: 1, state: 'accepted' });
		expect(fetcher).toHaveBeenCalledOnce();
	});
	it('same-generation or occupied instructions cannot authorize a fresh attempt', async () => {
		for (const data of [
			{ ...instructions, generation: 1 },
			{ ...instructions, can_submit: false }
		]) {
			const fetcher = vi
				.fn<typeof fetch>()
				.mockResolvedValueOnce(Response.json({}, { status: 404 }))
				.mockResolvedValueOnce(Response.json(data));
			await expect(
				inspectRequestAttempt(
					createRequestApiClient({ fetcher }),
					'fixture',
					token,
					await attempt(),
					new AbortController().signal
				)
			).rejects.toMatchObject({ status: 404 });
		}
	});
});
