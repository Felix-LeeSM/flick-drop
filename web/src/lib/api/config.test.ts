import { describe, expect, it, vi } from 'vitest';
import {
	DEFAULT_MAX_FILE_BYTES,
	DEFAULT_PAYLOAD_INLINE_MAX_BYTES,
	defaultLimits,
	getConfig,
	getVerifiedConfig
} from './config';

describe('getConfig', () => {
	it('parses the server-provided limits', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ payload_inline_max_bytes: 2048, max_file_bytes: 999_999 }), {
				status: 200
			})
		);

		const limits = await getConfig('http://api.local/', fetcher);

		expect(fetcher).toHaveBeenCalledWith('http://api.local/api/config');
		expect(limits).toEqual({ payloadInlineMaxBytes: 2048, maxFileBytes: 999_999 });
	});

	it('reserves the inline AES-GCM tag but preserves the plaintext file limit when offline', async () => {
		const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error('offline'));

		await expect(getConfig('http://api.local/', fetcher)).resolves.toEqual({
			payloadInlineMaxBytes: 1_048_560,
			maxFileBytes: 52_428_800
		});
	});

	it('falls back to defaults on a non-200 response', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('', { status: 500 }));

		await expect(getConfig('http://api.local/', fetcher)).resolves.toEqual(defaultLimits());
	});

	it('falls back to defaults for unparseable values', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ payload_inline_max_bytes: 'nope', max_file_bytes: -1 }), {
				status: 200
			})
		);

		const limits = await getConfig('http://api.local/', fetcher);

		expect(limits).toEqual({
			payloadInlineMaxBytes: DEFAULT_PAYLOAD_INLINE_MAX_BYTES,
			maxFileBytes: DEFAULT_MAX_FILE_BYTES
		});
	});

	it('normalizes a trailing slash in the base url', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ payload_inline_max_bytes: 1, max_file_bytes: 2 }), {
				status: 200
			})
		);

		await getConfig('http://api.local///', fetcher);

		expect(fetcher).toHaveBeenCalledWith('http://api.local/api/config');
	});
});

describe('verified config before one-time retrieval', () => {
	it('keeps larger custom limits instead of silently using advisory defaults', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(
				Response.json({ payload_inline_max_bytes: 4_194_288, max_file_bytes: 8_388_608 })
			);
		expect(await getVerifiedConfig('https://api.example.test/', fetcher)).toEqual({
			payloadInlineMaxBytes: 4_194_288,
			maxFileBytes: 8_388_608
		});
		expect(fetcher.mock.calls[0][1]).toMatchObject({
			credentials: 'omit',
			cache: 'no-store',
			redirect: 'error'
		});
	});
	it.each([
		{},
		null,
		{ payload_inline_max_bytes: '1048576', max_file_bytes: 100 },
		{ payload_inline_max_bytes: 0, max_file_bytes: 100 },
		{ payload_inline_max_bytes: 12.5, max_file_bytes: 100 },
		{ payload_inline_max_bytes: 100, max_file_bytes: Number.MAX_SAFE_INTEGER }
	])('rejects invalid config without a fallback', async (body) => {
		await expect(
			getVerifiedConfig('/', vi.fn<typeof fetch>().mockResolvedValue(Response.json(body)))
		).rejects.toThrow('Nothing has been opened.');
	});
	it('fails closed on config transport and HTTP failures', async () => {
		for (const fetcher of [
			vi.fn<typeof fetch>().mockRejectedValue(new Error('transport details')),
			vi.fn<typeof fetch>().mockResolvedValue(Response.json({}, { status: 503 }))
		]) {
			await expect(getVerifiedConfig('/', fetcher)).rejects.toThrow(
				'Could not verify size limits.'
			);
		}
	});
});
