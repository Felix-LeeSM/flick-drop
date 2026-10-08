import { describe, expect, it, vi } from 'vitest';
import {
	createManagementApiClient,
	createManagementUrl,
	managementTokenFromFragment
} from './management';

const token = 'A'.repeat(43);
const snapshot = {
	id: 'fixture',
	status: 'active',
	expires_at: '2099-01-01T00:00:00Z',
	management_expires_at: '2099-01-01T00:00:00Z',
	can_cancel: true
};

describe('management capability boundary', () => {
	it('places only a canonical token in the management fragment', () => {
		const url = new URL(createManagementUrl('https://flick.example.test', 'fixture', token));
		expect(url.pathname).toBe('/m/fixture');
		expect(url.search).toBe('');
		expect(url.hash).toBe(`#manage=${token}`);
		expect(managementTokenFromFragment(url.hash)).toBe(token);
	});
	it.each([
		'',
		'#key=secret',
		`#manage=${token}&key=secret`,
		`#manage=${token}&manage=${token}`,
		`#manage=${token}=`,
		`#manage=${'A'.repeat(42)}B`
	])('rejects incomplete or mixed fragments: %s', (fragment) => {
		expect(managementTokenFromFragment(fragment)).toBeNull();
	});
	it('uses an explicit authorization header without URL credentials, cookies or redirects', async () => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(snapshot));
		const client = createManagementApiClient({ baseUrl: 'https://api.example.test/', fetcher });
		expect(await client.get('fixture', token)).toEqual(snapshot);
		const [url, init] = fetcher.mock.calls[0];
		expect(url).toBe('https://api.example.test/api/secrets/fixture/management');
		expect(init).toMatchObject({
			method: 'GET',
			credentials: 'omit',
			cache: 'no-store',
			redirect: 'error',
			headers: { authorization: `Bearer ${token}` }
		});
		expect(init?.body).toBeUndefined();
	});
	it('submits an empty revoke body and surfaces the server winner on an open/cancel race', async () => {
		const opened = { ...snapshot, status: 'opened', can_cancel: false };
		const fetcher = vi
			.fn<typeof fetch>()
			.mockResolvedValue(
				Response.json({ error: { code: 'not_cancellable' }, status: opened }, { status: 409 })
			);
		const client = createManagementApiClient({ baseUrl: '/', fetcher });
		await expect(client.revoke('fixture', token)).rejects.toMatchObject({
			code: 'not_cancellable',
			status: 409,
			snapshot: opened
		});
		expect(fetcher.mock.calls[0][0]).toBe('/api/secrets/fixture/revoke');
		expect(fetcher.mock.calls[0][1]).toMatchObject({
			method: 'POST',
			body: '{}',
			headers: { authorization: `Bearer ${token}` }
		});
	});
	it.each([404, 429, 503])('keeps HTTP %i distinct from a delivery outcome', async (status) => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json({}, { status }));
		await expect(
			createManagementApiClient({ fetcher }).get('fixture', token)
		).rejects.toMatchObject({ status });
	});
	it('does not leak the failing transport error or invent an opened status', async () => {
		const fetcher = vi
			.fn<typeof fetch>()
			.mockRejectedValue(new Error('sensitive transport detail'));
		await expect(
			createManagementApiClient({ fetcher }).get('fixture', token)
		).rejects.toMatchObject({ code: 'network_error', snapshot: undefined });
	});
	it.each([
		{ ...snapshot, id: 'another' },
		{ ...snapshot, status: 'opened' },
		{ ...snapshot, expires_at: 'invalid' },
		{ ...snapshot, management_expires_at: '2100-01-01T00:00:00Z' }
	])('rejects an inconsistent snapshot', async (body) => {
		const fetcher = vi.fn<typeof fetch>().mockResolvedValue(Response.json(body));
		await expect(
			createManagementApiClient({ fetcher }).get('fixture', token)
		).rejects.toMatchObject({ code: 'invalid_response' });
	});
});
