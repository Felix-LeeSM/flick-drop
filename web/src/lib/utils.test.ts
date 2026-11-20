import { describe, expect, it } from 'vitest';

import { formatBytes } from './utils';

describe('formatBytes', () => {
	it('keeps whole bytes below one KiB', () => {
		expect(formatBytes(0)).toBe('0 B');
		expect(formatBytes(1023)).toBe('1023 B');
	});

	it('switches units at the binary boundaries', () => {
		expect(formatBytes(1024)).toBe('1.0 KiB');
		expect(formatBytes(1024 * 1024)).toBe('1.00 MiB');
	});

	// 1,048,560 is /api/config's limit with storage off; exact 50 MiB would pass either way.
	it('rounds down so a ceiling never reads larger than it is', () => {
		expect(formatBytes(1_048_560)).toBe('1023.9 KiB');
		expect(formatBytes(1_048_575)).toBe('1023.9 KiB');
		expect(formatBytes(52_428_000)).toBe('49.99 MiB');
		expect(formatBytes(52_428_800)).toBe('50.00 MiB');
	});
});
