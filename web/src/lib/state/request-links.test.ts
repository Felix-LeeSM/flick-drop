import { afterEach, describe, expect, it } from 'vitest';
import vectors from '../../../../tests/fixtures/request-crypto-v1.json';
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
afterEach(clearRequestHandoff);

describe('request link custody', () => {
	it('keeps private key and retrieval authority out of submission links and HTTP URL parts', () => {
		const submit = new URL(submissionUrl(origin, 'fixture', token, vectors.fingerprint));
		const receive = new URL(
			retrievalUrl(origin, 'fixture', `${'B'.repeat(42)}A`, vectors.private_key)
		);
		expect(submissionFromFragment(submit.hash)).toEqual({
			token,
			fingerprint: vectors.fingerprint
		});
		expect(retrievalFromFragment(receive.hash)?.privateKey).toBe(vectors.private_key);
		expect(submit.pathname).toBe('/r/fixture');
		expect(receive.pathname).toBe('/r/fixture/receive');
		expect(submit.search + receive.search).toBe('');
		expect(submit.href).not.toContain(vectors.private_key);
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
		expect(retrievalFromFragment(`#receive=${token}`)).toEqual({ token, privateKey: '' });
		for (const fragment of [
			`#receive=${token}&key=${'A'.repeat(4097)}`,
			`#receive=${token}&key=x&submit=${token}`,
			`#receive=${token}&key=x%2Fy`,
			`#receive=${token}&key=x=`
		]) {
			expect(retrievalFromFragment(fragment)).toBeNull();
		}
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
