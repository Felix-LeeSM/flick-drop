import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { build } from 'vite';

const vectors = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/request-crypto-v1.json', import.meta.url))
);
let browser;
let page;
before(async () => {
	const bundle = await build({
		configFile: false,
		logLevel: 'error',
		build: {
			write: false,
			lib: { entry: fileURLToPath(new URL('./requests.ts', import.meta.url)), formats: ['es'] }
		}
	});
	const output = Array.isArray(bundle) ? bundle[0] : bundle;
	const entry = output.output.find((item) => item.type === 'chunk' && item.isEntry);
	if (!entry) {
		throw new Error('Vite must emit a request crypto entry chunk');
	}
	browser = await chromium.launch();
	page = await browser.newPage();
	// Intercept the entire HTTPS origin: secure-context Web Crypto, with no listener or network.
	await page.route('https://crypto.test/**', (route) =>
		route.fulfill(
			route.request().url().endsWith('/requests.js')
				? { contentType: 'text/javascript', body: entry.code }
				: { contentType: 'text/html', body: '<!doctype html><title>Crypto test</title>' }
		)
	);
	await page.goto('https://crypto.test/');
});
after(async () => {
	await browser?.close();
});

test('native browser Web Crypto imports pinned keys, decrypts golden vectors, and rejects tampering', async () => {
	const result = await page.evaluate(async (v) => {
		const crypto = await import('/requests.js');
		const limits = { maxTextBytes: 1_048_560, maxFileBytes: 52_428_800 };
		const publicKey = await crypto.importRequestPublicKey(v.public_key, v.fingerprint);
		const privateKey = await crypto.importRequestPrivateKey(v.private_key, v.fingerprint);
		const text = await crypto.decryptRequestPayload(
			v.request_id,
			privateKey,
			v.text.payload,
			limits
		);
		const file = await crypto.decryptRequestPayload(
			v.request_id,
			privateKey,
			v.file.payload,
			limits
		);
		const pair = await crypto.generateRequestKeys();
		const exported = await crypto.exportRequestPublicKey(pair.publicKey);
		const restored = await crypto.importRequestPrivateKey(
			await crypto.exportRequestPrivateKey(pair.privateKey),
			exported.fingerprint
		);
		const encrypted = await crypto.encryptRequestPayload(
			v.request_id,
			pair.publicKey,
			{ kind: 'file', bytes: new Uint8Array([0, 255, 128]), filename: 'browser.txt' },
			limits
		);
		const opened = await crypto.decryptRequestPayload(v.request_id, restored, encrypted, limits);
		let rejections = 0;
		for (const operation of [
			() => crypto.importRequestPublicKey(v.public_key, exported.fingerprint),
			() => crypto.importRequestPrivateKey(v.private_key, exported.fingerprint),
			() => crypto.decryptRequestPayload(v.cross_request_id, privateKey, v.text.payload, limits),
			() =>
				crypto.decryptRequestPayload(v.request_id, privateKey, v.invalid_aes128_payload, limits),
			() =>
				crypto.decryptRequestPayload(
					v.request_id,
					privateKey,
					{
						...v.text.payload,
						ciphertext: `${v.text.payload.ciphertext[0] === 'A' ? 'B' : 'A'}${v.text.payload.ciphertext.slice(1)}`
					},
					limits
				)
		]) {
			try {
				await operation();
			} catch {
				rejections += 1;
			}
		}
		const canonicalPublic = await crypto.exportRequestPublicKey(publicKey);
		return {
			textMatches: text.text === v.text.plaintext,
			fileMatches:
				new TextDecoder().decode(file.bytes) === v.file.plaintext &&
				file.filename === v.file.filename,
			roundTripMatches:
				opened.kind === 'file' &&
				[...opened.bytes].join() === '0,255,128' &&
				opened.filename === 'browser.txt',
			canonicalPublic:
				canonicalPublic.public_key === v.public_key &&
				canonicalPublic.fingerprint === v.fingerprint,
			canonicalPrivate: (await crypto.exportRequestPrivateKey(privateKey)) === v.private_key,
			rejections
		};
	}, vectors);
	assert.deepEqual(result, {
		textMatches: true,
		fileMatches: true,
		roundTripMatches: true,
		canonicalPublic: true,
		canonicalPrivate: true,
		rejections: 5
	});
});
