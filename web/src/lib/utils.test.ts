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

	// The advertised upload limit is the inline threshold minus the AES-GCM tag.
	// Rounding up displayed it as "1024.0 KiB": a size the server refuses, and a
	// unit short of reading as 1 MiB.
	it('rounds down so a ceiling never reads larger than it is', () => {
		expect(formatBytes(1_048_560)).toBe('1023.9 KiB');
		expect(formatBytes(52_428_800)).toBe('50.00 MiB');
		expect(formatBytes(1_048_575)).toBe('1023.9 KiB');
	});
});
