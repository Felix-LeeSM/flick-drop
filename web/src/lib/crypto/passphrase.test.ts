import { afterEach, describe, expect, it, vi } from 'vitest';
import { generatePassphrase } from './passphrase';

const FORMAT = /^[0-9a-hjkmnp-tv-z]{4}(-[0-9a-hjkmnp-tv-z]{4}){3}$/;

// Replaces the next random fill with the given bytes.
function nextRandomBytes(values: number[]) {
	return vi.spyOn(crypto, 'getRandomValues').mockImplementationOnce((array) => {
		(array as Uint8Array).set(values);
		return array;
	});
}

describe('generatePassphrase', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('returns four groups of four lowercase Crockford base32 symbols', () => {
		expect(generatePassphrase()).toMatch(FORMAT);
	});

	it('maps the low five bits of each of 16 random bytes to one symbol', () => {
		// Odd positions set the three high bits, which must not change the symbol.
		const low = Array.from({ length: 16 }, (_, index) => index | (index % 2 ? 0xe0 : 0));
		const spy = nextRandomBytes(low);
		expect(generatePassphrase()).toBe('0123-4567-89ab-cdef');
		expect(spy).toHaveBeenCalledOnce();
		expect((spy.mock.calls[0][0] as Uint8Array).length).toBe(16);

		nextRandomBytes(Array.from({ length: 16 }, (_, index) => index + 16));
		expect(generatePassphrase()).toBe('ghjk-mnpq-rstv-wxyz');
	});

	it('draws fresh randomness on every call', () => {
		const values = new Set(Array.from({ length: 64 }, () => generatePassphrase()));
		expect(values.size).toBe(64);
	});
});
