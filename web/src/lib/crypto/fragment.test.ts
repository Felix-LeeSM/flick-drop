import { describe, expect, it } from 'vitest';

import { generateSecretIdentity } from './age';
import { decodeKeyFragment, encodeKeyFragment } from './fragment';

describe('fragment identity transport', () => {
	it('round trips a hybrid identity through the fragment', async () => {
		const identity = await generateSecretIdentity();
		const fragment = encodeKeyFragment(identity);

		expect(fragment).toBe(`key=${identity}`);
		expect(decodeKeyFragment(fragment)).toBe(identity);
		expect(decodeKeyFragment(`#${fragment}`)).toBe(identity);
	});

	it('refuses to encode anything but a hybrid identity', () => {
		expect(() => encodeKeyFragment('AGE-SECRET-KEY-1QQQQ')).toThrow();
	});

	it('returns null for missing or malformed identities', () => {
		expect(decodeKeyFragment('')).toBeNull();
		expect(decodeKeyFragment('other=value')).toBeNull();
		expect(decodeKeyFragment('#key=')).toBeNull();
		// The AES-GCM format's base64url raw key is no longer a valid fragment.
		expect(decodeKeyFragment('key=cHFyc3R1dnd4eXp7fH1-f4CBgoOEhYaHiImKi4yNjo8')).toBeNull();
		expect(decodeKeyFragment('key=AGE-SECRET-KEY-1QQQQ')).toBeNull();
		expect(decodeKeyFragment('key=AGE-SECRET-KEY-PQ-1bad!')).toBeNull();
	});

	it('rejects over-length fragments (DoS guard, #89)', () => {
		expect(decodeKeyFragment(`key=AGE-SECRET-KEY-PQ-1${'Q'.repeat(128)}`)).toBeNull();
	});
});
