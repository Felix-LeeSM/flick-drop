import { describe, expect, it, vi } from 'vitest';
import { createStatsApiClient, type StatsReport } from './stats';

const report: StatsReport = {
	days: 2,
	series: [
		{ day: '2026-11-09', metrics: { secret_created_text: 1 } },
		{ day: '2026-11-10', metrics: { secret_created_text: 3, bytes_uploaded: 4096 } }
	],
	totals: { secret_created_text: 4, bytes_uploaded: 4096 },
	live: { active_secrets: 1, live_requests: 2 }
};

describe('stats api client', () => {
	it('sends the admin token only in the authorization header with no-store and no redirects', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(report));
		const client = createStatsApiClient({ baseUrl: 'https://api.example.test/', fetcher });
		expect(await client.get('stats-token', 30)).toEqual(report);
		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('https://api.example.test/api/stats?days=30');
		expect(init).toMatchObject({
			method: 'GET',
			credentials: 'omit',
			cache: 'no-store',
			redirect: 'error',
			headers: { authorization: 'Bearer stats-token' }
		});
		expect(init?.body).toBeUndefined();
	});
	it('maps a 404 to an unavailable endpoint, hiding whether the token or the deploy failed', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 404 }));
		const client = createStatsApiClient({ fetcher });
		await expect(client.get('wrong-token')).rejects.toMatchObject({
			code: 'stats_unavailable',
			status: 404
		});
	});
	it('rejects a malformed report instead of rendering guessed numbers', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(Response.json({ days: 30, series: [{ day: 'nope' }], totals: {} }));
		const client = createStatsApiClient({ fetcher });
		await expect(client.get('stats-token')).rejects.toMatchObject({ code: 'invalid_response' });
	});
	it('maps transport failures to a network error without retrying', async () => {
		const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new TypeError('offline'));
		const client = createStatsApiClient({ fetcher });
		await expect(client.get('stats-token')).rejects.toMatchObject({ code: 'network_error' });
		expect(fetcher).toHaveBeenCalledTimes(1);
	});
});
