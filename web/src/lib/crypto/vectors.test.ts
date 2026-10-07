// Golden vectors shared with internal/clientcrypto (the Go CLI's copy of these
// primitives). Both suites read tests/fixtures/client-crypto-vectors.json, so a
// constant that moves on one side — iteration count, nonce size, the access
// verifier purpose string, the filename envelope shape — fails here or in
// internal/clientcrypto/vectors_test.go instead of silently producing links one
// client can create and the other cannot open.
//
// Do not "fix" a failure here by regenerating the fixture. A changed vector
// means the wire format changed, which breaks every link already in flight.

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { decodeKeyFragment, encodeKeyFragment } from './fragment';
import {
	base64ToBytes,
	decryptFileWithKey,
	decryptText,
	decryptTextWithKey,
	deriveAccessProof,
	encryptFileWithKey,
	encryptText,
	encryptTextWithKey,
	generateSecretKey,
	importAesGcmKey,
	KDF_ITERATIONS,
	type KdfParams,
	NONCE_BYTES,
	RAW_KEY_BYTES,
	SALT_BYTES
} from './text';

type Vectors = {
	text_model_a: {
		passphrase: string;
		plaintext: string;
		kdf: KdfParams;
		nonce: string;
		ciphertext: string;
	};
	access_proof: { passphrase: string; kdf: KdfParams; proof: string };
	text_model_b: { key: string; plaintext: string; nonce: string; ciphertext: string };
	file_model_b: {
		key: string;
		plaintext: string;
		nonce: string;
		ciphertext: string;
		filename: string;
		filename_nonce: string;
		encrypted_filename: string;
		content_type: string;
	};
	fragment: { key: string; fragment: string };
};

const vectors = JSON.parse(
	readFileSync(
		new URL('../../../../tests/fixtures/client-crypto-vectors.json', import.meta.url),
		'utf8'
	)
) as Vectors;

describe('golden vectors shared with the Go client', () => {
	it('reproduces the model A text ciphertext from a passphrase', async () => {
		const v = vectors.text_model_a;

		const encrypted = await encryptText(v.plaintext, v.passphrase, {
			salt: base64ToBytes(v.kdf.salt),
			nonce: base64ToBytes(v.nonce),
			iterations: v.kdf.iterations
		});

		expect(encrypted.ciphertext).toBe(v.ciphertext);
		expect(encrypted.kdf).toEqual(v.kdf);
	});

	it('decrypts the model A vector back to its plaintext', async () => {
		const v = vectors.text_model_a;

		const plaintext = await decryptText(
			{ ciphertext: v.ciphertext, nonce: v.nonce, kdf: v.kdf, size_bytes: 0 },
			v.passphrase
		);

		expect(plaintext).toBe(v.plaintext);
	});

	it('reproduces the access proof', async () => {
		const v = vectors.access_proof;

		await expect(deriveAccessProof(v.passphrase, v.kdf)).resolves.toBe(v.proof);
	});

	it('reproduces the model B text ciphertext from a raw key', async () => {
		const v = vectors.text_model_b;
		const key = await importAesGcmKey(base64ToBytes(v.key));

		const encrypted = await encryptTextWithKey(v.plaintext, key, {
			nonce: base64ToBytes(v.nonce)
		});

		expect(encrypted.ciphertext).toBe(v.ciphertext);
		await expect(decryptTextWithKey(encrypted, key)).resolves.toBe(v.plaintext);
	});

	it('reproduces the model B file ciphertext and filename envelope', async () => {
		const v = vectors.file_model_b;
		const key = await importAesGcmKey(base64ToBytes(v.key));
		const file = new File([v.plaintext], v.filename, { type: v.content_type });

		const encrypted = await encryptFileWithKey(file, key, {
			nonce: base64ToBytes(v.nonce),
			filenameNonce: base64ToBytes(v.filename_nonce)
		});

		expect(encrypted.ciphertext).toBe(v.ciphertext);
		expect(encrypted.content_type).toBe(v.content_type);
		// Compared as parsed fields, not raw JSON: both clients parse the
		// envelope, so key order is not part of the contract.
		expect(JSON.parse(encrypted.encrypted_filename)).toEqual(JSON.parse(v.encrypted_filename));
	});

	it('decrypts the model B file vector back to its body and filename', async () => {
		const v = vectors.file_model_b;
		const key = await importAesGcmKey(base64ToBytes(v.key));

		const decrypted = await decryptFileWithKey(
			{
				ciphertext: v.ciphertext,
				nonce: v.nonce,
				kdf: { algorithm: 'PBKDF2-SHA-256', salt: '', iterations: 0, key_length_bits: 256 },
				size_bytes: 0,
				encrypted_filename: v.encrypted_filename,
				content_type: v.content_type
			},
			key
		);

		expect(new TextDecoder().decode(decrypted.bytes)).toBe(v.plaintext);
		expect(decrypted.filename).toBe(v.filename);
		expect(decrypted.contentType).toBe(v.content_type);
	});

	it('round trips the share URL fragment key', () => {
		const v = vectors.fragment;
		const raw = base64ToBytes(v.key);

		expect(encodeKeyFragment(raw)).toBe(v.fragment);
		expect(decodeKeyFragment(`#${v.fragment}`)).toEqual(raw);
	});

	// Every vector supplies its own salt and nonce, so the fixture pins how
	// those bytes are used but not how many are generated. A one-sided change
	// to the salt or nonce size would leave both suites green and surface only
	// as an AEAD tag mismatch between clients, so the sizes are asserted here
	// and in internal/clientcrypto/vectors_test.go.
	it('pins the salt, nonce, and key sizes the generators must produce', async () => {
		expect(base64ToBytes(vectors.text_model_a.kdf.salt)).toHaveLength(SALT_BYTES);
		expect(base64ToBytes(vectors.access_proof.kdf.salt)).toHaveLength(SALT_BYTES);
		expect(base64ToBytes(vectors.text_model_a.nonce)).toHaveLength(NONCE_BYTES);
		expect(base64ToBytes(vectors.text_model_b.nonce)).toHaveLength(NONCE_BYTES);
		expect(base64ToBytes(vectors.file_model_b.nonce)).toHaveLength(NONCE_BYTES);
		expect(base64ToBytes(vectors.file_model_b.filename_nonce)).toHaveLength(NONCE_BYTES);
		expect(base64ToBytes(vectors.text_model_b.key)).toHaveLength(RAW_KEY_BYTES);
		expect(base64ToBytes(vectors.fragment.key)).toHaveLength(RAW_KEY_BYTES);

		// The half the fixture cannot see: what the generators actually emit.
		const encrypted = await encryptText('x', 'passphrase');
		expect(base64ToBytes(encrypted.kdf.salt)).toHaveLength(SALT_BYTES);
		expect(base64ToBytes(encrypted.nonce)).toHaveLength(NONCE_BYTES);
		expect(encrypted.kdf.iterations).toBe(KDF_ITERATIONS);

		const { raw } = await generateSecretKey();
		expect(raw).toHaveLength(RAW_KEY_BYTES);
	});
});
