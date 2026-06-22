import { describe, expect, it } from 'vitest';

import { decodeKeyFragment, encodeKeyFragment } from './fragment';

describe('fragment key transport', () => {
	it('round trips raw key bytes through the fragment', () => {
		// Bytes chosen to exercise base64url edge cases (+/- and padding).
		const raw = new Uint8Array([1, 2, 3, 250, 255, 0, 127, 191, 254]);
		const fragment = encodeKeyFragment(raw);

		expect(fragment.startsWith('key=')).toBe(true);
		expect(fragment).not.toContain('#');
		expect(fragment).not.toContain('+');
		expect(fragment).not.toContain('/');
		expect(decodeKeyFragment(fragment)).toEqual(raw);
	});

	it('decodes a fragment with or without the leading hash', () => {
		const raw = new Uint8Array([10, 20, 30, 40]);
		const fragment = encodeKeyFragment(raw);
		expect(decodeKeyFragment(fragment)).toEqual(raw);
		expect(decodeKeyFragment(`#${fragment}`)).toEqual(raw);
	});

	it('returns null for missing or malformed keys', () => {
		expect(decodeKeyFragment('')).toBeNull();
		expect(decodeKeyFragment('other=value')).toBeNull();
		expect(decodeKeyFragment('#other=value')).toBeNull();
		expect(decodeKeyFragment('key=')).toBeNull();
		expect(decodeKeyFragment('#key=')).toBeNull();
		expect(decodeKeyFragment('key=!!!!')).toBeNull();
	});

	it('rejects over-length fragments (DoS guard, #89)', () => {
		expect(decodeKeyFragment(`key=${'A'.repeat(128)}`)).toBeNull();
		expect(decodeKeyFragment(`#key=${'A'.repeat(128)}`)).toBeNull();
	});
});
