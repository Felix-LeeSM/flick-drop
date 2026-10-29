import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import { generateRequestKeys } from '$lib/crypto/requests';
import {
	clearRequestHandoff,
	handOffRequest,
	retrievalFromFragment,
	retrievalUrl,
	submissionFromFragment,
	submissionUrl,
	takeRequest
} from './request-links';

const token = 'A'.repeat(43);
const origin = 'https://flick.example.test';
// The fingerprint is a fixed synthetic value; only its shape matters here.
const vectors = { fingerprint: `${'F'.repeat(42)}A`, identity: '' };
beforeAll(async () => {
	vectors.identity = (await generateRequestKeys()).identity;
});
afterEach(clearRequestHandoff);

describe('request link custody', () => {
	it('keeps private key and retrieval authority out of submission links and HTTP URL parts', () => {
		const submit = new URL(submissionUrl(origin, 'fixture', token, vectors.fingerprint));
		const receive = new URL(
			retrievalUrl(origin, 'fixture', `${'B'.repeat(42)}A`, vectors.identity)
		);
		expect(submissionFromFragment(submit.hash)).toEqual({
			token,
			fingerprint: vectors.fingerprint
		});
		expect(retrievalFromFragment(receive.hash)?.identity).toBe(vectors.identity);
		expect(submit.pathname).toBe('/r/fixture');
		expect(receive.pathname).toBe('/r/fixture/receive');
		expect(submit.search + receive.search).toBe('');
		expect(submit.href).not.toContain(vectors.identity);
		expect(submit.href).not.toContain('receive=');
		expect(receive.href).not.toContain('submit=');
	});
	it.each([
		'',
		'#submit=bad',
		`#submit=${token}&fp=${vectors.fingerprint}&key=secret`,
		`#submit=${token}&fp=${vectors.fingerprint}&submit=${token}`,
		`#fp=${vectors.fingerprint}&submit=${token}`,
		`#submit=${'A'.repeat(42)}B&fp=${vectors.fingerprint}`
	])('rejects noncanonical/mixed submission fragments %s', (fragment) => {
		expect(submissionFromFragment(fragment)).toBeNull();
	});
	it('distinguishes missing owner key from a malformed or oversized fragment without decoding it', () => {
		expect(retrievalFromFragment(`#receive=${token}`)).toEqual({ token, identity: '' });
		for (const fragment of [
			`#receive=${token}&key=AGE-SECRET-KEY-PQ-1${'Q'.repeat(200)}`,
			`#receive=${token}&key=x&submit=${token}`,
			`#receive=${token}&key=x%2Fy`,
			`#receive=${token}&key=x=`
		]) {
			expect(retrievalFromFragment(fragment)).toBeNull();
		}
		// A damaged key still parses, so the page can say so.
		expect(retrievalFromFragment(`#receive=${token}&key=invalid`)?.identity).toBe('invalid');
	});
	it('consumes a single memory-only handoff for one matching owner, without retaining old links', () => {
		const created = {
			id: 'fixture',
			token,
			submissionUrl: submissionUrl(origin, 'fixture', token, vectors.fingerprint)
		};
		handOffRequest(created);
		expect(takeRequest('fixture', token)).toEqual(created);
		expect(takeRequest('fixture', token)).toBeNull();
		handOffRequest(created);
		expect(takeRequest('fixture', 'other')).toBeNull();
		expect(takeRequest('fixture', token)).toBeNull();
		handOffRequest(created);
		clearRequestHandoff();
		expect(takeRequest('fixture', token)).toBeNull();
	});
});
