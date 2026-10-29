import { beforeAll, describe, expect, it } from 'vitest';
import { encryptTo, generateSecretIdentity, recipientOf } from './age';
import { bytesToBase64 } from './encoding';
import {
	decryptRequestPayload,
	encryptedFilenameFromHeader,
	encryptRequestPayload,
	generateRequestKeys,
	importRequestIdentity,
	importRequestPublicKey,
	type OpenedRequestBody,
	requestFingerprint,
	type SealedRequest
} from './requests';

const limits = { maxTextBytes: 1024, maxFileBytes: 200_000 };
let keys: Awaited<ReturnType<typeof generateRequestKeys>>;
beforeAll(async () => {
	keys = await generateRequestKeys();
});

// What the API returns from open: the body as a stream, the filename header decoded.
function opened(sealed: SealedRequest): OpenedRequestBody {
	return {
		kind: sealed.kind,
		encryptedFilename: sealed.envelope.encrypted_filename
			? encryptedFilenameFromHeader(sealed.envelope.encrypted_filename)
			: undefined,
		body: sealed.body.stream()
	};
}
const age = (bytes: Uint8Array) => new TextDecoder().decode(bytes.subarray(0, 22));

describe('request keys', () => {
	it('publishes the hybrid recipient with a SHA-256 fingerprint of its exact string', async () => {
		expect(keys.identity).toMatch(/^AGE-SECRET-KEY-PQ-1/);
		expect(keys.public_key).toBe(await recipientOf(keys.identity));
		expect(keys.public_key).toMatch(/^age1pq1/);
		expect(keys.fingerprint).toBe(await requestFingerprint(keys.public_key));
		expect(keys.fingerprint).toMatch(/^[A-Za-z0-9_-]{43}$/);
	});
	it('pins the submitter to the fingerprint in its link', async () => {
		await expect(importRequestPublicKey(keys.public_key, keys.fingerprint)).resolves.toBe(
			keys.public_key
		);
		const other = await generateRequestKeys();
		await expect(importRequestPublicKey(other.public_key, keys.fingerprint)).rejects.toThrow();
		for (const bad of [
			'',
			keys.public_key.toUpperCase(),
			`${keys.public_key}\n`,
			`${keys.public_key}${'q'.repeat(2400)}`,
			await recipientOf(await generateSecretIdentity()).then((r) => r.replace('age1pq1', 'age1'))
		]) {
			await expect(importRequestPublicKey(bad, await requestFingerprint(bad))).rejects.toThrow();
		}
	});
	it('accepts only the identity whose recipient has the stored fingerprint', async () => {
		await expect(importRequestIdentity(keys.identity, keys.fingerprint)).resolves.toBe(
			keys.identity
		);
		const other = await generateRequestKeys();
		await expect(importRequestIdentity(other.identity, keys.fingerprint)).rejects.toThrow();
		for (const bad of [
			'',
			'AGE-SECRET-KEY-1QQQQ',
			keys.identity.toLowerCase(),
			`${keys.identity}Q`
		]) {
			await expect(importRequestIdentity(bad, keys.fingerprint)).rejects.toThrow();
		}
	});
});

describe('request payloads', () => {
	it('round-trips text as an age file whose length is size_bytes', async () => {
		const sealed = await encryptRequestPayload(
			keys.public_key,
			{ kind: 'text', text: 'héllo' },
			limits
		);
		const body = new Uint8Array(await sealed.body.arrayBuffer());
		expect(sealed).toMatchObject({ kind: 'text', plaintextBytes: 6, envelope: { version: 2 } });
		expect(sealed.envelope).not.toHaveProperty('encrypted_filename');
		expect(sealed.size_bytes).toBe(body.length);
		expect(age(body)).toBe('age-encryption.org/v1\n');
		await expect(decryptRequestPayload(keys.identity, opened(sealed), limits)).resolves.toEqual({
			kind: 'text',
			text: 'héllo'
		});
	});
	it('round-trips a multi-chunk file and its UTF-8 name as an octet-stream Blob', async () => {
		const bytes = Uint8Array.from({ length: 150_001 }, (_, i) => i % 251);
		const sealed = await encryptRequestPayload(
			keys.public_key,
			{ kind: 'file', file: new File([bytes], 'résumé.pdf', { type: 'text/html' }) },
			limits
		);
		expect(sealed.plaintextBytes).toBe(bytes.length);
		expect(sealed.size_bytes).toBe(sealed.body.size);
		const result = await decryptRequestPayload(keys.identity, opened(sealed), limits);
		if (result.kind !== 'file') {
			throw new Error('expected a file');
		}
		expect(result.filename).toBe('résumé.pdf');
		expect(result.blob.type).toBe('application/octet-stream');
		expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(bytes);
	});
	it('enforces plaintext limits and filename bounds before encrypting', async () => {
		await expect(
			encryptRequestPayload(keys.public_key, { kind: 'text', text: 'x'.repeat(1025) }, limits)
		).rejects.toThrow();
		await expect(
			encryptRequestPayload(
				keys.public_key,
				{ kind: 'file', file: new File(['x'], 'a'.repeat(1025)) },
				limits
			)
		).rejects.toThrow();
		await expect(
			encryptRequestPayload(keys.public_key, { kind: 'file', file: new File(['x'], '') }, limits)
		).rejects.toThrow();
	});
	it('rejects another identity, a truncated body, and kind/filename mismatches', async () => {
		const sealed = await encryptRequestPayload(
			keys.public_key,
			{ kind: 'file', file: new File(['secret bytes'], 'a.txt') },
			limits
		);
		const other = await generateRequestKeys();
		await expect(decryptRequestPayload(other.identity, opened(sealed), limits)).rejects.toThrow();
		const truncated = {
			...opened(sealed),
			body: sealed.body.slice(0, sealed.body.size - 1).stream()
		};
		await expect(decryptRequestPayload(keys.identity, truncated, limits)).rejects.toThrow();
		await expect(
			decryptRequestPayload(keys.identity, { ...opened(sealed), kind: 'text' }, limits)
		).rejects.toThrow();
		await expect(
			decryptRequestPayload(
				keys.identity,
				{ ...opened(sealed), encryptedFilename: undefined },
				limits
			)
		).rejects.toThrow();
	});
	it('refuses decrypted content above the advertised limit', async () => {
		const body = await encryptTo(keys.identity, 'x'.repeat(2000));
		await expect(
			decryptRequestPayload(
				keys.identity,
				{ kind: 'text', body: new Blob([body as Uint8Array<ArrayBuffer>]).stream() },
				limits
			)
		).rejects.toThrow();
	});
	it('accepts only canonical base64 filename headers', () => {
		const bytes = new Uint8Array([1, 2, 3]);
		expect(encryptedFilenameFromHeader(bytesToBase64(bytes))).toEqual(bytes);
		for (const bad of ['', 'AQID=', 'AQI', '****', bytesToBase64(new Uint8Array(4097))]) {
			expect(() => encryptedFilenameFromHeader(bad)).toThrow();
		}
	});
});
