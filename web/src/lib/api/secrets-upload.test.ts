import { afterEach, describe, expect, it, vi } from 'vitest';
import { type EncryptedFilePayload, KDF_ALGORITHM } from '$lib/crypto/text';
import { createSecretApiClient } from './secrets';

const payload: EncryptedFilePayload = {
	ciphertext: 'AAECAw==',
	nonce: '',
	kdf: { algorithm: KDF_ALGORITHM, salt: '', iterations: 0, key_length_bits: 256 },
	size_bytes: 1000,
	encrypted_filename: 'encrypted',
	content_type: 'application/octet-stream'
};
const staged = {
	id: 'large-id',
	expires_at: '2099-01-01T00:00:00Z',
	upload: {
		url: 'https://object-store.local/upload',
		method: 'PUT',
		headers: { 'content-length': '4', 'x-amz-meta-probe': 'signed' }
	}
};

// Only the native transport boundary is replaced; the API's orchestration runs.
class UploadRequest {
	status = 204;
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

function setup() {
	const xhr = new UploadRequest();
	vi.stubGlobal(
		'XMLHttpRequest',
		class {
			constructor() {
				// biome-ignore lint/correctness/noConstructorReturn: expose the native transport double to the test.
				return xhr;
			}
		}
	);
	const finalize = Promise.withResolvers<Response>();
	const fetcher = vi
		.fn<typeof fetch>()
		.mockResolvedValueOnce(Response.json(staged))
		.mockImplementationOnce(() => finalize.promise);
	const client = createSecretApiClient({
		fetcher,
		limits: { payloadInlineMaxBytes: 100, maxFileBytes: 2000 }
	});
	return { xhr, fetcher, client, finalize };
}

afterEach(() => vi.unstubAllGlobals());

describe('native encrypted upload', () => {
	it('reports actual bytes, keeps unknown totals indeterminate, and waits for finalize at 100%', async () => {
		const { xhr, fetcher, client, finalize } = setup();
		const progress = vi.fn();
		const result = client.createFileSecret(payload, 600, undefined, undefined, progress);
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		expect(xhr.open).toHaveBeenCalledWith('PUT', staged.upload.url);
		expect(xhr.setRequestHeader.mock.calls).toEqual([['x-amz-meta-probe', 'signed']]);
		expect(new Uint8Array(xhr.send.mock.calls[0][0])).toEqual(new Uint8Array([0, 1, 2, 3]));
		xhr.upload.onprogress?.({ loaded: 2, total: 4, lengthComputable: true });
		expect(progress).toHaveBeenLastCalledWith({ stage: 'uploading', loaded: 2, total: 4 });
		xhr.upload.onprogress?.({ loaded: 3, total: 0, lengthComputable: false });
		expect(progress).toHaveBeenLastCalledWith({ stage: 'uploading', loaded: 3, total: undefined });
		xhr.upload.onprogress?.({ loaded: 4, total: 4, lengthComputable: true });
		expect(fetcher).toHaveBeenCalledTimes(1);
		let completed = false;
		void result.then(() => {
			completed = true;
		});
		xhr.onload?.();
		await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
		expect(progress).toHaveBeenLastCalledWith({ stage: 'finalizing' });
		expect(completed).toBe(false);
		finalize.resolve(Response.json({ finalized: true }));
		await expect(result).resolves.toEqual({ id: staged.id, expires_at: staged.expires_at });
	});

	it('aborts the PUT and ignores captured late callbacks without finalizing', async () => {
		const { xhr, fetcher, client } = setup();
		const controller = new AbortController();
		const progress = vi.fn();
		const result = client.createFileSecret(payload, 600, undefined, controller.signal, progress);
		const cancelled = expect(result).rejects.toMatchObject({ code: 'upload_cancelled' });
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		const lateProgress = xhr.upload.onprogress;
		const lateLoad = xhr.onload;
		controller.abort();
		await cancelled;
		const count = progress.mock.calls.length;
		lateProgress?.({ loaded: 4, total: 4, lengthComputable: true });
		lateLoad?.();
		expect(progress).toHaveBeenCalledTimes(count);
		expect(xhr.abort).toHaveBeenCalled();
		expect(fetcher).toHaveBeenCalledTimes(1);
		expect(xhr.upload.onprogress).toBeNull();
	});

	it.each([
		'http',
		'network',
		'timeout'
	] as const)('rejects %s failures without finalizing', async (failure) => {
		const { xhr, fetcher, client } = setup();
		const result = client.createFileSecret(payload, 600);
		const rejected = expect(result).rejects.toMatchObject({
			code: failure === 'http' ? 'upload_failed' : 'network_error'
		});
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		if (failure === 'http') {
			xhr.status = 403;
			xhr.onload?.();
		} else if (failure === 'network') {
			xhr.onerror?.();
		} else {
			xhr.ontimeout?.();
		}
		await rejected;
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it('does not create when already cancelled or finalize when cancelled after upload', async () => {
		const { xhr, fetcher, client } = setup();
		const before = new AbortController();
		before.abort();
		await expect(
			client.createFileSecret(payload, 600, undefined, before.signal)
		).rejects.toMatchObject({ code: 'upload_cancelled' });
		expect(fetcher).not.toHaveBeenCalled();
		const controller = new AbortController();
		const result = client.createFileSecret(payload, 600, undefined, controller.signal);
		const cancelled = expect(result).rejects.toMatchObject({ code: 'upload_cancelled' });
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		xhr.onload?.();
		controller.abort();
		await cancelled;
		expect(fetcher).toHaveBeenCalledTimes(1);
	});

	it.each([
		'failure',
		'abort'
	] as const)('does not return a link after finalize %s', async (failure) => {
		const { xhr, fetcher, client, finalize } = setup();
		const controller = new AbortController();
		const result = client.createFileSecret(payload, 600, undefined, controller.signal);
		const rejected = expect(result).rejects.toMatchObject({
			code: failure === 'abort' ? 'upload_cancelled' : 'not_ready'
		});
		await vi.waitFor(() => expect(xhr.send).toHaveBeenCalledOnce());
		xhr.onload?.();
		await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
		if (failure === 'abort') {
			controller.abort();
		}
		finalize.resolve(
			failure === 'abort'
				? Response.json({ finalized: true })
				: Response.json({ error: { code: 'not_ready' } }, { status: 503 })
		);
		await rejected;
		expect(fetcher).toHaveBeenCalledTimes(2);
	});
});
