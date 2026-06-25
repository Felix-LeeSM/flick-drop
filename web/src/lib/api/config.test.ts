import { describe, expect, it, vi } from 'vitest';
import {
	DEFAULT_MAX_FILE_BYTES,
	DEFAULT_PAYLOAD_INLINE_MAX_BYTES,
	defaultLimits,
	getConfig
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

	it('falls back to defaults when the fetch fails', async () => {
		const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error('offline'));

		await expect(getConfig('http://api.local/', fetcher)).resolves.toEqual(defaultLimits());
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
