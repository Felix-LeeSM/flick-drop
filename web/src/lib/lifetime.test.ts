import { describe, expect, it } from 'vitest';

import { remainingSecondsFrom } from './lifetime';

describe('remainingSecondsFrom', () => {
	it('returns 0 for empty expiresAt', () => {
		expect(remainingSecondsFrom('', 1_000_000)).toBe(0);
	});

	it('returns remaining seconds for a future expiry', () => {
		const now = Date.UTC(2026, 0, 1, 0, 0, 0);
		const expires = new Date(now + 90_000).toISOString();
		expect(remainingSecondsFrom(expires, now)).toBe(90);
	});

	it('clamps a past expiry to 0', () => {
		const now = Date.UTC(2026, 0, 1, 0, 0, 0);
		const expires = new Date(now - 90_000).toISOString();
		expect(remainingSecondsFrom(expires, now)).toBe(0);
	});

	it('returns 0 for an unparseable expiresAt instead of NaN (#88)', () => {
		expect(remainingSecondsFrom('not-a-date', 1_000_000)).toBe(0);
		expect(remainingSecondsFrom('2026-13-99T00:00:00Z', 1_000_000)).toBe(0);
	});
});
