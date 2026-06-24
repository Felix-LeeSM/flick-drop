import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { formatTtlRange } from './ttl';

describe('ttl limits', () => {
	beforeEach(() => {
		vi.resetModules();
		vi.unstubAllEnvs();
	});

	afterEach(() => {
		vi.unstubAllEnvs();
	});

	it('uses defaults when PUBLIC_ env is unset', async () => {
		const mod = await import('./ttl');
		expect(mod.MIN_TTL_SECONDS).toBe(300);
		expect(mod.MAX_TTL_SECONDS).toBe(604_800);
		expect(mod.DEFAULT_TTL_SECONDS).toBe(3600);
	});

	it('reads PUBLIC_ overrides', async () => {
		vi.stubEnv('PUBLIC_FLICK_MIN_TTL_SECONDS', '120');
		vi.stubEnv('PUBLIC_FLICK_MAX_TTL_SECONDS', '86400');
		vi.stubEnv('PUBLIC_FLICK_DEFAULT_TTL_SECONDS', '600');
		const mod = await import('./ttl');
		expect(mod.MIN_TTL_SECONDS).toBe(120);
		expect(mod.MAX_TTL_SECONDS).toBe(86_400);
		expect(mod.DEFAULT_TTL_SECONDS).toBe(600);
	});

	it('falls back to defaults for non-positive or unparseable values', async () => {
		vi.stubEnv('PUBLIC_FLICK_MIN_TTL_SECONDS', '0');
		vi.stubEnv('PUBLIC_FLICK_MAX_TTL_SECONDS', 'nope');
		vi.stubEnv('PUBLIC_FLICK_DEFAULT_TTL_SECONDS', '-5');
		const mod = await import('./ttl');
		expect(mod.MIN_TTL_SECONDS).toBe(300);
		expect(mod.MAX_TTL_SECONDS).toBe(604_800);
		expect(mod.DEFAULT_TTL_SECONDS).toBe(3600);
	});
});

describe('formatTtlRange', () => {
	it('formats the default range', () => {
		expect(formatTtlRange(300, 604_800)).toBe('Choose a lifetime between 5 minutes and 7 days.');
	});

	it('formats a non-default range', () => {
		expect(formatTtlRange(60, 86_400)).toBe('Choose a lifetime between 1 minute and 1 day.');
	});
});
