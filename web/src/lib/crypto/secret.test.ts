import { describe, expect, it } from 'vitest';
import { generateSecretIdentity } from './age';
import {
	openFile,
	openText,
	sealFile,
	sealText,
	unwrapIdentityOffThread,
	wrapIdentityOffThread
} from './secret';

// age encrypts in 64 KiB chunks with a 16-byte tag each (contracts/age-format-v1.md).
const CHUNK = 64 * 1024;
const TAG = 16;
// Two full chunks plus a short final one.
const FILE_BYTES = 2 * CHUNK + 18_928;

const streamOf = (bytes: Uint8Array) => new Blob([bytes as Uint8Array<ArrayBuffer>]).stream();
const patternFile = () =>
	new File([new Uint8Array(FILE_BYTES).map((_, i) => i % 251)], 'quarterly report.pdf', {
		type: 'application/pdf'
	});

describe('sealing and opening secrets', () => {
	it('round-trips text', async () => {
		expect.assertions(1);
		const identity = await generateSecretIdentity();
		const body = await sealText(identity, 'héllo, flick ✓');
		await expect(openText(identity, streamOf(body))).resolves.toBe('héllo, flick ✓');
	});

	it('round-trips a multi-chunk file with its name and type', async () => {
		expect.assertions(5);
		const identity = await generateSecretIdentity();
		const file = patternFile();

		const sealed = await sealFile(identity, file);
		expect(sealed).toMatchObject({ contentType: 'application/pdf', plaintextBytes: FILE_BYTES });
		expect(sealed.body.size).toBeGreaterThan(FILE_BYTES + 3 * TAG);

		const opened = await openFile(
			identity,
			sealed.body.stream(),
			sealed.encryptedFilename,
			sealed.contentType
		);
		expect(opened.filename).toBe('quarterly report.pdf');
		expect(opened.blob.type).toBe('application/pdf');
		expect(new Uint8Array(await opened.blob.arrayBuffer())).toEqual(
			new Uint8Array(await file.arrayBuffer())
		);
	});

	it('rejects a truncated text body instead of returning partial text', async () => {
		expect.assertions(1);
		const identity = await generateSecretIdentity();
		const body = await sealText(identity, 'a message that loses its tail');
		await expect(openText(identity, streamOf(body.subarray(0, body.length - 1)))).rejects.toThrow();
	});

	it.each([
		['a byte', 1],
		// Drops the whole final chunk, so what remains ends on a chunk boundary.
		['the final chunk', FILE_BYTES - 2 * CHUNK + TAG]
	])('rejects a file body missing %s', async (_, missing) => {
		expect.assertions(1);
		const identity = await generateSecretIdentity();
		const sealed = await sealFile(identity, patternFile());
		await expect(
			openFile(
				identity,
				sealed.body.slice(0, sealed.body.size - missing).stream(),
				sealed.encryptedFilename,
				sealed.contentType
			)
		).rejects.toThrow();
	});

	it('rejects the wrong identity', async () => {
		expect.assertions(2);
		const identity = await generateSecretIdentity();
		const other = await generateSecretIdentity();
		const text = await sealText(identity, 'not for you');
		const file = await sealFile(identity, new File(['x'], 'x.txt'));
		await expect(openText(other, streamOf(text))).rejects.toThrow();
		await expect(
			openFile(other, file.body.stream(), file.encryptedFilename, file.contentType)
		).rejects.toThrow();
	});
});

// Vitest runs in node, where Worker is undefined, so these take the direct
// path at the fixed work factor 18 (about a second per scrypt run).
describe('Model A identity wrapping', () => {
	it('round-trips and rejects a wrong passphrase', { timeout: 60_000 }, async () => {
		expect.assertions(2);
		const identity = await generateSecretIdentity();
		const wrapped = await wrapIdentityOffThread(identity, 'correct horse battery staple');
		await expect(unwrapIdentityOffThread(wrapped, 'correct horse battery staple')).resolves.toBe(
			identity
		);
		await expect(unwrapIdentityOffThread(wrapped, 'wrong horse')).rejects.toThrow();
	});
});
