// The access_proof block of tests/fixtures/client-crypto-vectors.json is also
// checked by internal/clientcrypto (DeriveAccessProof). A failure here means the
// browser and the Go CLI derive different proofs for the same passphrase, so
// one client could not open the other's Model A secrets. Do not regenerate the
// fixture to make it pass.

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
	assertKdf,
	createAccessVerifier,
	deriveAccessProof,
	KDF_ITERATIONS,
	KDF_MAX_ITERATIONS,
	type KdfParams
} from './access';
import { base64ToBytes } from './encoding';

const vector = (
	JSON.parse(
		readFileSync(
			new URL('../../../../tests/fixtures/client-crypto-vectors.json', import.meta.url),
			'utf8'
		)
	) as { access_proof: { passphrase: string; kdf: KdfParams; proof: string } }
).access_proof;

describe('access proof', () => {
	it('reproduces the shared golden vector', async () => {
		expect.assertions(1);
		await expect(deriveAccessProof(vector.passphrase, vector.kdf)).resolves.toBe(vector.proof);
	});

	it('creates a verifier whose kdf and proof match the vector for the same salt', async () => {
		expect.assertions(1);
		await expect(
			createAccessVerifier(vector.passphrase, { salt: base64ToBytes(vector.kdf.salt) })
		).resolves.toEqual({ kdf: vector.kdf, proof: vector.proof });
	});

	it('rejects an empty passphrase', async () => {
		expect.assertions(1);
		await expect(deriveAccessProof('', vector.kdf)).rejects.toThrow();
	});

	it.each([
		['another algorithm', { algorithm: 'PBKDF2-SHA-1' }],
		['fewer iterations', { iterations: KDF_ITERATIONS - 1 }],
		['more iterations than the cap', { iterations: KDF_MAX_ITERATIONS + 1 }],
		['a fractional iteration count', { iterations: KDF_ITERATIONS + 0.5 }],
		['a missing iteration count', { iterations: undefined }],
		['a shorter key', { key_length_bits: 128 }],
		['an empty salt', { salt: '' }]
	])('rejects %s', async (_, change) => {
		expect.assertions(2);
		const kdf = { ...vector.kdf, ...change } as KdfParams;
		expect(() => assertKdf(kdf)).toThrow('Unsupported KDF parameters');
		await expect(deriveAccessProof(vector.passphrase, kdf)).rejects.toThrow(
			'Unsupported KDF parameters'
		);
	});
});
