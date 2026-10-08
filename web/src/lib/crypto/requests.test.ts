import { generateKeyPairSync } from 'node:crypto';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import vectors from '../../../../tests/fixtures/request-crypto-v1.json';
import {
	decryptRequestPayload,
	encryptRequestPayload,
	exportRequestPrivateKey,
	exportRequestPublicKey,
	generateRequestKeys,
	importRequestPrivateKey,
	importRequestPublicKey,
	parseRequestEnvelope,
	type RequestPayload,
	serializeRequestEnvelope
} from './requests';
import { base64ToBytes, bytesToBase64 } from './text';

const limits = { maxTextBytes: 1_048_560, maxFileBytes: 52_428_800 };
let publicKey: CryptoKey;
let privateKey: CryptoKey;
beforeAll(async () => {
	publicKey = await importRequestPublicKey(vectors.public_key, vectors.fingerprint);
	privateKey = await importRequestPrivateKey(vectors.private_key, vectors.fingerprint);
});
afterEach(() => vi.restoreAllMocks());
const decrypt = (payload: unknown, id = vectors.request_id) =>
	decryptRequestPayload(id, privateKey, payload, limits);
const flipped = (encoded: string) => {
	const bytes = base64ToBytes(encoded);
	bytes[0] ^= 1;
	return bytesToBase64(bytes);
};

describe('request v1 immutable golden vectors', () => {
	it('pins SPKI fingerprint and PKCS8 export without exposing private fields in the public result', async () => {
		expect(await exportRequestPublicKey(publicKey)).toEqual({
			public_key: vectors.public_key,
			fingerprint: vectors.fingerprint
		});
		expect(await exportRequestPrivateKey(privateKey)).toBe(vectors.private_key);
	});
	it('decrypts independent text and file vectors including UTF-8 filenames', async () => {
		await expect(decrypt(vectors.text.payload)).resolves.toEqual({
			kind: 'text',
			text: vectors.text.plaintext
		});
		await expect(decrypt(vectors.file.payload)).resolves.toEqual({
			kind: 'file',
			bytes: new TextEncoder().encode(vectors.file.plaintext),
			filename: vectors.file.filename
		});
	});
	it('serializes a validated copy and does not mutate the input envelope', () => {
		const envelope = structuredClone(vectors.file.payload.envelope);
		const parsed = parseRequestEnvelope(serializeRequestEnvelope(envelope, 'file'), 'file');
		expect(parsed).toEqual(envelope);
		expect(parsed).not.toBe(envelope);
		expect(parsed.encrypted_filename).not.toBe(envelope.encrypted_filename);
	});
	it('rejects cross-request and cross-kind replay', async () => {
		await expect(decrypt(vectors.text.payload, vectors.cross_request_id)).rejects.toThrow();
		await expect(
			decrypt({
				...vectors.text.payload,
				kind: 'file',
				envelope: {
					...vectors.text.payload.envelope,
					encrypted_filename: vectors.file.payload.envelope.encrypted_filename
				}
			})
		).rejects.toThrow();
	});
	it('rejects a wrapped AES-128 key rather than downgrading', async () => {
		await expect(decrypt(vectors.invalid_aes128_payload)).rejects.toThrow();
	});
	it('binds AES payloads to request and kind even when the content key is rewrapped', async () => {
		const label = (id: string, kind: string) =>
			new TextEncoder().encode(`Flick request v1\n${id}\n${kind}\nkey`);
		const raw = await crypto.subtle.decrypt(
			{ name: 'RSA-OAEP', label: label(vectors.request_id, 'text') },
			privateKey,
			new Uint8Array(base64ToBytes(vectors.text.payload.envelope.wrapped_key))
		);
		for (const [id, kind] of [
			[vectors.cross_request_id, 'text'],
			[vectors.request_id, 'file']
		] as const) {
			const wrapped = await crypto.subtle.encrypt(
				{ name: 'RSA-OAEP', label: label(id, kind) },
				publicKey,
				raw
			);
			await expect(
				decrypt(
					{
						...vectors.text.payload,
						kind,
						envelope: {
							...vectors.text.payload.envelope,
							wrapped_key: bytesToBase64(new Uint8Array(wrapped)),
							...(kind === 'file' && {
								encrypted_filename: vectors.file.payload.envelope.encrypted_filename
							})
						}
					},
					id
				)
			).rejects.toThrow();
		}
	});
	it('rejects swapping payload and filename purposes with otherwise valid lengths', async () => {
		const original = vectors.file.payload;
		const filename = original.envelope.encrypted_filename;
		await expect(
			decrypt({
				...original,
				size_bytes: base64ToBytes(filename.ciphertext).length - 16,
				ciphertext: filename.ciphertext,
				envelope: {
					...original.envelope,
					nonce: filename.nonce,
					encrypted_filename: { nonce: original.envelope.nonce, ciphertext: original.ciphertext }
				}
			})
		).rejects.toThrow();
	});
	it.each([
		'wrapped_key',
		'nonce',
		'ciphertext',
		'filename_nonce',
		'filename_ciphertext'
	])('authenticates %s', async (field) => {
		const changed = structuredClone(vectors.file.payload);
		if (field === 'ciphertext') {
			changed.ciphertext = flipped(changed.ciphertext);
		} else if (field === 'filename_nonce') {
			changed.envelope.encrypted_filename.nonce = flipped(
				changed.envelope.encrypted_filename.nonce
			);
		} else if (field === 'filename_ciphertext') {
			changed.envelope.encrypted_filename.ciphertext = flipped(
				changed.envelope.encrypted_filename.ciphertext
			);
		} else if (field === 'wrapped_key') {
			changed.envelope.wrapped_key = flipped(changed.envelope.wrapped_key);
		} else {
			changed.envelope.nonce = flipped(changed.envelope.nonce);
		}
		await expect(decrypt(changed)).rejects.toThrow();
	});
});

describe('request key validation', () => {
	it('round-trips generated keys and pins both import paths to the expected public key', async () => {
		const pair = await generateRequestKeys();
		const exported = await exportRequestPublicKey(pair.publicKey);
		const pkcs8 = await exportRequestPrivateKey(pair.privateKey);
		expect(exported.fingerprint).not.toBe(vectors.fingerprint);
		await expect(
			importRequestPublicKey(exported.public_key, exported.fingerprint)
		).resolves.toHaveProperty('type', 'public');
		await expect(importRequestPrivateKey(pkcs8, exported.fingerprint)).resolves.toHaveProperty(
			'type',
			'private'
		);
		await expect(
			importRequestPublicKey(exported.public_key, vectors.fingerprint)
		).rejects.toThrow();
		await expect(importRequestPrivateKey(pkcs8, vectors.fingerprint)).rejects.toThrow();
		await expect(
			decryptRequestPayload(vectors.request_id, pair.privateKey, vectors.text.payload, limits)
		).rejects.toThrow();
	});
	it.each([
		['weak modulus', 1024, 65_537],
		['wrong exponent', 2048, 3]
	] as const)('rejects %s in public and private DER', async (_, modulusLength, publicExponent) => {
		const pair = generateKeyPairSync('rsa', { modulusLength, publicExponent });
		await expect(
			importRequestPublicKey(
				pair.publicKey.export({ type: 'spki', format: 'der' }).toString('base64'),
				vectors.fingerprint
			)
		).rejects.toThrow();
		await expect(
			importRequestPrivateKey(
				pair.privateKey.export({ type: 'pkcs8', format: 'der' }).toString('base64url'),
				vectors.fingerprint
			)
		).rejects.toThrow();
	});
	it('rejects non-RSA key types, trailing DER, and noncanonical encodings', async () => {
		const pair = generateKeyPairSync('ec', { namedCurve: 'prime256v1' });
		await expect(
			importRequestPublicKey(
				pair.publicKey.export({ type: 'spki', format: 'der' }).toString('base64'),
				vectors.fingerprint
			)
		).rejects.toThrow();
		await expect(
			importRequestPrivateKey(
				pair.privateKey.export({ type: 'pkcs8', format: 'der' }).toString('base64url'),
				vectors.fingerprint
			)
		).rejects.toThrow();
		await expect(
			importRequestPublicKey(`${vectors.public_key}\n`, vectors.fingerprint)
		).rejects.toThrow();
		await expect(
			importRequestPrivateKey(`${vectors.private_key}=`, vectors.fingerprint)
		).rejects.toThrow();
		const withTrailing = Buffer.concat([
			Buffer.from(vectors.private_key, 'base64url'),
			Buffer.from([0])
		]);
		await expect(
			importRequestPrivateKey(withTrailing.toString('base64url'), vectors.fingerprint)
		).rejects.toThrow();
		await expect(
			importRequestPublicKey(
				Buffer.concat([Buffer.from(vectors.public_key, 'base64'), Buffer.from([0])]).toString(
					'base64'
				),
				vectors.fingerprint
			)
		).rejects.toThrow();
	});
	it('rejects structurally valid but unusable private RSA components before open', async () => {
		const jwk = await crypto.subtle.exportKey('jwk', privateKey);
		const bad = await crypto.subtle.importKey(
			'jwk',
			{ ...jwk, d: 'AQ', dp: 'AQ', dq: 'AQ', qi: 'AQ' },
			{ name: 'RSA-OAEP', hash: 'SHA-256' },
			true,
			['decrypt']
		);
		const encoded = Buffer.from(await crypto.subtle.exportKey('pkcs8', bad)).toString('base64url');
		await expect(importRequestPrivateKey(encoded, vectors.fingerprint)).rejects.toThrow();
	});
	it('bounds encoded inputs before key import and rejects malformed fingerprints', async () => {
		const importer = vi.spyOn(crypto.subtle, 'importKey');
		await expect(importRequestPrivateKey('A'.repeat(4097), vectors.fingerprint)).rejects.toThrow();
		await expect(importRequestPublicKey('A'.repeat(4097), vectors.fingerprint)).rejects.toThrow();
		expect(importer).not.toHaveBeenCalled();
		await expect(
			importRequestPublicKey(vectors.public_key, `${vectors.fingerprint}=`)
		).rejects.toThrow();
		await expect(importRequestPrivateKey(vectors.private_key, 'A')).rejects.toThrow();
	});
});

describe('request envelope validation before decryption', () => {
	it.each([
		{ version: 2 },
		{ algorithm: 'RSA-OAEP-256+A128GCM' },
		{ kdf: {} },
		{ nonce: bytesToBase64(new Uint8Array(11)) },
		{ wrapped_key: bytesToBase64(new Uint8Array(255)) },
		{ nonce: `${vectors.text.payload.envelope.nonce}\n` },
		{ wrapped_key: `${vectors.text.payload.envelope.wrapped_key.slice(0, -3)}B==` },
		{ encrypted_filename: vectors.file.payload.envelope.encrypted_filename }
	])('rejects invalid envelope %j', async (patch) => {
		const primitive = vi.spyOn(crypto.subtle, 'decrypt');
		await expect(
			decrypt({ ...vectors.text.payload, envelope: { ...vectors.text.payload.envelope, ...patch } })
		).rejects.toThrow();
		expect(primitive).not.toHaveBeenCalled();
	});
	it.each([
		-1,
		0.5,
		Number.MAX_SAFE_INTEGER,
		Number.NaN,
		'3'
	])('rejects invalid declared size %s before crypto', async (size) => {
		const primitive = vi.spyOn(crypto.subtle, 'decrypt');
		await expect(decrypt({ ...vectors.text.payload, size_bytes: size })).rejects.toThrow();
		expect(primitive).not.toHaveBeenCalled();
	});
	it('rejects kind/filename mismatches, nonce reuse, filename bounds, unknown fields, and overlong JSON', async () => {
		expect(() => parseRequestEnvelope(vectors.text.payload.envelope, 'file')).toThrow();
		for (const encryptedFilename of [
			{
				...vectors.file.payload.envelope.encrypted_filename,
				nonce: vectors.file.payload.envelope.nonce
			},
			{ nonce: vectors.file.payload.envelope.nonce, ciphertext: bytesToBase64(new Uint8Array(16)) },
			{
				...vectors.file.payload.envelope.encrypted_filename,
				ciphertext: bytesToBase64(new Uint8Array(1041))
			},
			{ ...vectors.file.payload.envelope.encrypted_filename, extra: true }
		]) {
			expect(() =>
				parseRequestEnvelope(
					{ ...vectors.file.payload.envelope, encrypted_filename: encryptedFilename },
					'file'
				)
			).toThrow();
		}
		expect(() => parseRequestEnvelope(' '.repeat(4097), 'text')).toThrow();
		await expect(decrypt({ ...vectors.text.payload, extra: true })).rejects.toThrow();
		await expect(decrypt({ ...vectors.text.payload, kind: 'credential' })).rejects.toThrow();
		await expect(
			decrypt({ ...vectors.text.payload, size_bytes: vectors.text.payload.size_bytes + 1 })
		).rejects.toThrow();
		await expect(
			decrypt({ ...vectors.text.payload, ciphertext: `${vectors.text.payload.ciphertext}AA==` })
		).rejects.toThrow();
	});
	it.each([
		'',
		'req\nother',
		'req\rother',
		'\ud800',
		'a'.repeat(257)
	])('rejects ambiguous or oversized request IDs', async (id) => {
		await expect(decrypt(vectors.text.payload, id)).rejects.toThrow();
	});
});

describe('request encryption', () => {
	it('generates fresh AES keys and nonces for each attempt', async () => {
		const first = await encryptRequestPayload(
			vectors.request_id,
			publicKey,
			{ kind: 'text', text: 'same text' },
			limits
		);
		const second = await encryptRequestPayload(
			vectors.request_id,
			publicKey,
			{ kind: 'text', text: 'same text' },
			limits
		);
		const label = new TextEncoder().encode(`Flick request v1\n${vectors.request_id}\ntext\nkey`);
		const unwrap = (payload: RequestPayload) =>
			crypto.subtle.decrypt(
				{ name: 'RSA-OAEP', label },
				privateKey,
				new Uint8Array(base64ToBytes(payload.envelope.wrapped_key))
			);
		expect(new Uint8Array(await unwrap(first))).not.toEqual(new Uint8Array(await unwrap(second)));
		expect(first.envelope.nonce).not.toBe(second.envelope.nonce);
		await expect(decrypt(first)).resolves.toEqual({ kind: 'text', text: 'same text' });
	});
	it('round-trips empty text, binary files, and the UTF-8 filename limit', async () => {
		const text = await encryptRequestPayload(
			vectors.request_id,
			publicKey,
			{ kind: 'text', text: '' },
			limits
		);
		await expect(decrypt(text)).resolves.toEqual({ kind: 'text', text: '' });
		const content = {
			kind: 'file' as const,
			bytes: new Uint8Array([0, 255, 128]),
			filename: 'é'.repeat(512)
		};
		const encrypted = await encryptRequestPayload(vectors.request_id, publicKey, content, limits);
		await expect(decrypt(encrypted)).resolves.toEqual(content);
		expect(encrypted.envelope.encrypted_filename?.nonce).not.toBe(encrypted.envelope.nonce);
		expect(Object.keys(encrypted)).toEqual(['kind', 'size_bytes', 'envelope', 'ciphertext']);
	});
	it('preserves a leading UTF-8 BOM in text and filenames', async () => {
		for (const content of [
			{ kind: 'text' as const, text: '\uFEFFtext' },
			{ kind: 'file' as const, bytes: new Uint8Array([1]), filename: '\uFEFFname.txt' }
		]) {
			const encrypted = await encryptRequestPayload(vectors.request_id, publicKey, content, limits);
			await expect(decrypt(encrypted)).resolves.toEqual(content);
		}
	});
	it('enforces explicit caller limits and filename byte limits before encryption', async () => {
		const primitive = vi.spyOn(crypto.subtle, 'encrypt');
		await expect(
			encryptRequestPayload(vectors.request_id, publicKey, { kind: 'text', text: '\ud800' }, limits)
		).rejects.toThrow();
		await expect(
			encryptRequestPayload(
				vectors.request_id,
				publicKey,
				{ kind: 'text', text: 'ab' },
				{ ...limits, maxTextBytes: 1 }
			)
		).rejects.toThrow();
		for (const filename of ['', 'é'.repeat(513), '\ud800']) {
			await expect(
				encryptRequestPayload(
					vectors.request_id,
					publicKey,
					{ kind: 'file', bytes: new Uint8Array(), filename },
					limits
				)
			).rejects.toThrow();
		}
		await expect(
			encryptRequestPayload(
				vectors.request_id,
				publicKey,
				{ kind: 'file', bytes: new Uint8Array(2), filename: 'x' },
				{ ...limits, maxFileBytes: 1 }
			)
		).rejects.toThrow();
		expect(primitive).not.toHaveBeenCalled();
	});
	it('rejects equal random nonces without encrypting', async () => {
		vi.spyOn(crypto, 'getRandomValues').mockImplementation((array) => {
			new Uint8Array((array as Uint8Array).buffer).fill(7);
			return array;
		});
		await expect(
			encryptRequestPayload(
				vectors.request_id,
				publicKey,
				{ kind: 'file', bytes: new Uint8Array([1]), filename: 'x' },
				limits
			)
		).rejects.toThrow();
	});
	it('rejects an RSA hash downgrade and unavailable Web Crypto', async () => {
		const weak = await crypto.subtle.importKey(
			'spki',
			new Uint8Array(base64ToBytes(vectors.public_key)),
			{ name: 'RSA-OAEP', hash: 'SHA-1' },
			true,
			['encrypt']
		);
		await expect(
			encryptRequestPayload(vectors.request_id, weak, { kind: 'text', text: 'x' }, limits)
		).rejects.toThrow();
		vi.stubGlobal('crypto', undefined);
		try {
			await expect(generateRequestKeys()).rejects.toThrow('secure context');
		} finally {
			vi.unstubAllGlobals();
		}
	});
});
