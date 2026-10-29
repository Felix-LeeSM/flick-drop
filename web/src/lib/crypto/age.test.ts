// tests/fixtures/age/vectors.json holds one block made by this module and one
// made by internal/clientcrypto/age.go. Both suites open both blocks, so a
// format choice that moves on one side (identity type, work factor, how the
// identity is wrapped) fails here or in internal/clientcrypto/age_test.go.

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
	decryptWith,
	encryptTo,
	generateSecretIdentity,
	IDENTITY_PREFIX,
	parseSecretIdentity,
	unwrapIdentity,
	wrapIdentity
} from './age';

type Side = {
	identity: string;
	wrapped_identity: string;
	text: string;
	filename: string;
	file: string;
};

const fixtures = new URL('../../../../tests/fixtures/age/', import.meta.url);
const vectors = JSON.parse(readFileSync(new URL('vectors.json', fixtures), 'utf8')) as {
	passphrase: string;
	plaintext: { text: string; filename: string; file: { length: number } };
	go: Side;
	web: Side;
};

const fromBase64 = (value: string) => new Uint8Array(Buffer.from(value, 'base64'));
const patternFile = (n: number) => new Uint8Array(n).map((_, i) => i % 251);
const decoder = new TextDecoder();

// contracts/age-format-v1.md: header, 16-byte nonce, plaintext, and one 16-byte
// tag per 64 KiB chunk (at least one chunk, even for an empty file).
function ciphertextSize(header: number, plaintext: number): number {
	return header + 16 + plaintext + 16 * Math.max(1, Math.ceil(plaintext / 65_536));
}

function headerLength(file: Uint8Array): number {
	const text = new TextDecoder('latin1').decode(file);
	const mac = text.indexOf('\n--- ');
	return text.indexOf('\n', mac + 1) + 1;
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<Uint8Array> {
	return new Uint8Array(await new Response(stream).arrayBuffer());
}

describe('age vectors open from both clients', () => {
	for (const name of ['go', 'web'] as const) {
		it(`opens the ${name} block`, async () => {
			const side = vectors[name];
			const identity = await unwrapIdentity(fromBase64(side.wrapped_identity), vectors.passphrase);
			expect(identity).toBe(side.identity);

			const text = await decryptWith(side.identity, fromBase64(side.text));
			expect(decoder.decode(text)).toBe(vectors.plaintext.text);
			const filename = await decryptWith(identity, fromBase64(side.filename));
			expect(decoder.decode(filename)).toBe(vectors.plaintext.filename);

			const file = new Uint8Array(readFileSync(new URL(side.file, fixtures)));
			const body = await readAll(await decryptWith(identity, new Blob([file]).stream()));
			expect(body).toEqual(patternFile(vectors.plaintext.file.length));
			expect(ciphertextSize(headerLength(file), body.length)).toBe(file.length);
		});
	}
});

describe('age round trip', () => {
	it('generates a short hybrid identity', async () => {
		const identity = await generateSecretIdentity();
		expect(identity.startsWith(IDENTITY_PREFIX)).toBe(true);
		expect(identity.length).toBeLessThan(100);
	});

	it('streams every chunk boundary and matches the size formula', async () => {
		const identity = await generateSecretIdentity();
		for (const n of [0, 1, 65_535, 65_536, 65_537, 3 * 65_536 + 7]) {
			const plaintext = patternFile(n);
			const ciphertext = await readAll(await encryptTo(identity, new Blob([plaintext]).stream()));
			expect(ciphertextSize(headerLength(ciphertext), n)).toBe(ciphertext.length);
			expect(await decryptWith(identity, ciphertext)).toEqual(plaintext);
		}
	});
});

describe('age fails closed', () => {
	it('rejects a wrong passphrase', async () => {
		await expect(
			unwrapIdentity(fromBase64(vectors.go.wrapped_identity), 'wrong passphrase')
		).rejects.toThrow();
	});

	it('rejects a wrong identity', async () => {
		const other = await generateSecretIdentity();
		await expect(decryptWith(other, fromBase64(vectors.go.text))).rejects.toThrow();
	});

	it('rejects a non-hybrid identity', () => {
		expect(() => parseSecretIdentity('AGE-SECRET-KEY-1QQQQ')).toThrow();
	});

	it('rejects a truncated file instead of returning a short plaintext', async () => {
		const file = new Uint8Array(readFileSync(new URL(vectors.web.file, fixtures)));
		const truncated = new Blob([file.subarray(0, file.length - 100)]).stream();
		await expect(
			decryptWith(vectors.web.identity, truncated).then((stream) => readAll(stream))
		).rejects.toThrow();
	});

	it('refuses a work factor above 20 before running scrypt', async () => {
		const wrapped = fromBase64(vectors.go.wrapped_identity);
		const text = new TextDecoder('latin1').decode(wrapped);
		expect(text).toContain(' 18\n');
		const heavy = Uint8Array.from(text.replace(' 18\n', ' 21\n'), (c) => c.charCodeAt(0));
		await expect(unwrapIdentity(heavy, vectors.passphrase)).rejects.toThrow(/work factor|logN|20/i);
	});

	it('wraps at work factor 18 and unwraps', async () => {
		const identity = await generateSecretIdentity();
		const wrapped = await wrapIdentity(identity, vectors.passphrase);
		expect(new TextDecoder('latin1').decode(wrapped)).toContain(' 18\n');
		expect(await unwrapIdentity(wrapped, vectors.passphrase)).toBe(identity);
	});
});
