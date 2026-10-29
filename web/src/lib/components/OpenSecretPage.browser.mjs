// biome-ignore-all lint/style/useNamingConvention: API fixture fields use the wire-format names.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

// access_proof is the PBKDF2 vector shared with the Go client; the age block
// made by this module is a real text file to the web identity, plus that
// identity wrapped under the same passphrase (both fixtures use
// "correct horse battery staple").
const { access_proof: accessProof } = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/client-crypto-vectors.json', import.meta.url))
);
const age = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/age/vectors.json', import.meta.url))
);
const plaintext = age.plaintext.text;
const modelBFragment = `#key=${age.web.identity}`;
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const secretPath = '/api/secrets/metadata-retry';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

test('open page shares the header: Create sits in the site navigation before the theme toggle', async () => {
	const page = await browser.newPage({ viewport: { width: 375, height: 800 } });
	try {
		await page.route(`**${secretPath}`, (route) =>
			route.fulfill({ status: 404, json: { error: { code: 'not_found' } } })
		);
		await page.goto(`${baseUrl}/s/metadata-retry`);
		await page
			.getByRole('navigation', { name: 'Site' })
			.getByRole('link', { name: 'Create', exact: true })
			.waitFor();
		const header = await page.evaluate(() => {
			const box = (element) => element.getBoundingClientRect().toJSON();
			const root = document.querySelector('header');
			const [badge, wordmark] = root.querySelector('a').children;
			return {
				badge: box(badge),
				wordmark: getComputedStyle(wordmark).fontSize,
				link: box(root.querySelector('nav a')),
				toggle: box(root.querySelector('button'))
			};
		});
		assert.deepEqual([header.badge.width, header.badge.height, header.wordmark], [32, 32, '27px']);
		assert.ok(header.link.right <= header.toggle.left);
	} finally {
		await page.close();
	}
});

for (const model of ['a', 'b']) {
	for (const failure of ['network', 503, 429]) {
		test(`Model ${model.toUpperCase()} recovers from ${failure} without a reload`, async () => {
			const page = await browser.newPage();
			try {
				let lookups = 0;
				let opens = 0;
				const pendingRetry = Promise.withResolvers();
				const retryReceived = Promise.withResolvers();
				await page.route(`**${secretPath}`, async (route) => {
					lookups += 1;
					if (lookups === 1) {
						if (failure === 'network') {
							await route.abort('failed');
						} else {
							await route.fulfill({ status: failure, json: { error: { code: 'request_failed' } } });
						}
						return;
					}
					retryReceived.resolve();
					await pendingRetry.promise;
					await route.fulfill({
						json: {
							id: 'metadata-retry',
							kind: 'text',
							size_bytes: Buffer.from(age.web.text, 'base64').length,
							expires_at: '2099-01-01T00:00:00Z',
							...(model === 'a' ? { access: { kdf: accessProof.kdf } } : {})
						}
					});
				});
				await page.route(`**${secretPath}/open`, async (route) => {
					opens += 1;
					assert.deepEqual(
						route.request().postDataJSON(),
						model === 'a' ? { access_proof: accessProof.proof } : {}
					);
					// contracts/openapi.yaml openSecret: the raw age body, metadata in headers.
					await route.fulfill({
						contentType: 'application/octet-stream',
						headers: {
							// The app calls the API cross-origin; internal/httpapi/router.go
							// exposes these headers the same way.
							'Access-Control-Expose-Headers':
								'Flick-Kind, Flick-Content-Type, Flick-Encrypted-Filename, Flick-Wrapped-Identity, Flick-Expires-At',
							'Flick-Kind': 'text',
							'Flick-Expires-At': '2099-01-01T00:00:00Z',
							...(model === 'a' ? { 'Flick-Wrapped-Identity': age.web.wrapped_identity } : {})
						},
						body: Buffer.from(age.web.text, 'base64')
					});
				});
				const fragment = model === 'b' ? modelBFragment : '';
				await page.goto(`${baseUrl}/s/metadata-retry${fragment}`);
				const retry = page.getByRole('button', { name: 'Retry loading' });
				await retry.waitFor();
				const open = page.getByRole('button', { name: 'Open', exact: true });
				assert.equal(await open.isEnabled(), false);
				await retry.click();
				await retryReceived.promise;
				await page.getByRole('status').filter({ hasText: 'Loading secret' }).waitFor();
				assert.equal(await open.isEnabled(), false);
				assert.equal(await retry.count(), 0);
				pendingRetry.resolve();
				if (model === 'a') {
					await page.getByLabel('passphrase', { exact: true }).fill(age.passphrase);
				} else {
					await page.getByText('This link opens without a passphrase.', { exact: false }).waitFor();
					assert.equal(await page.getByLabel('passphrase', { exact: true }).count(), 0);
				}
				await open.click();
				const secret = page.getByRole('textbox', { name: 'Decrypted secret' });
				await secret.waitFor();
				assert.equal(await secret.inputValue(), plaintext);
				assert.equal(lookups, 2);
				assert.equal(opens, 1);
			} finally {
				await page.close();
			}
		});
	}
}

for (const [status, phase] of [
	[404, 'metadata'],
	[410, 'metadata'],
	[410, 'open']
]) {
	test(`${phase} ${status} stays terminal`, async () => {
		const page = await browser.newPage();
		try {
			if (phase === 'open') {
				await page.route(`**${secretPath}`, (route) =>
					route.fulfill({
						json: {
							id: 'metadata-retry',
							kind: 'text',
							size_bytes: 1,
							expires_at: '2099-01-01T00:00:00Z'
						}
					})
				);
			}
			await page.route(`**${secretPath}${phase === 'open' ? '/open' : ''}`, (route) =>
				route.fulfill({
					status,
					json: { error: { code: status === 404 ? 'not_found' : 'consumed' } }
				})
			);
			await page.goto(`${baseUrl}/s/metadata-retry${modelBFragment}`);
			if (phase === 'open') {
				await page.getByRole('button', { name: 'Open', exact: true }).click();
			}
			await page.getByRole('link', { name: 'Create a new secret instead' }).waitFor();
			assert.equal(await page.getByRole('button', { name: 'Retry loading' }).count(), 0);
			assert.equal(
				await page.getByRole('button', { name: 'Open', exact: true }).isEnabled(),
				false
			);
		} finally {
			await page.close();
		}
	});
}

// Serves a Model B secret: metadata without an access block, then the age body.
async function routeModelB(page, kind, body, headers = {}) {
	await page.route(`**${secretPath}`, (route) =>
		route.fulfill({
			json: {
				id: 'metadata-retry',
				kind,
				size_bytes: body.length,
				expires_at: '2099-01-01T00:00:00Z'
			}
		})
	);
	await page.route(`**${secretPath}/open`, (route) =>
		route.fulfill({
			contentType: 'application/octet-stream',
			headers: {
				'Access-Control-Expose-Headers':
					'Flick-Kind, Flick-Content-Type, Flick-Encrypted-Filename, Flick-Wrapped-Identity, Flick-Expires-At',
				'Flick-Kind': kind,
				'Flick-Expires-At': '2099-01-01T00:00:00Z',
				...headers
			},
			body
		})
	);
}

test('Model B file opens to its filename and a download of the original bytes', async () => {
	const page = await browser.newPage();
	try {
		await routeModelB(
			page,
			'file',
			await readFile(new URL(`../../../../tests/fixtures/age/${age.web.file}`, import.meta.url)),
			{ 'Flick-Content-Type': 'application/pdf', 'Flick-Encrypted-Filename': age.web.filename }
		);
		await page.goto(`${baseUrl}/s/metadata-retry${modelBFragment}`);
		await page.getByRole('button', { name: 'Open', exact: true }).click();
		await page.getByText(age.plaintext.filename, { exact: true }).waitFor();
		const download = page.getByRole('link', { name: 'Download' });
		assert.equal(await download.getAttribute('download'), age.plaintext.filename);
		const blob = await download.evaluate(async (link) => {
			const response = await (await fetch(link.href)).blob();
			const bytes = new Uint8Array(await response.arrayBuffer());
			return {
				type: response.type,
				length: bytes.length,
				matches: bytes.every((byte, i) => byte === i % 251)
			};
		});
		assert.deepEqual(blob, {
			type: 'application/octet-stream',
			length: age.plaintext.file.length,
			matches: true
		});
	} finally {
		await page.close();
	}
});

test('a body that fails to authenticate after the open is final and shows nothing', async () => {
	const page = await browser.newPage();
	try {
		const text = Buffer.from(age.web.text, 'base64');
		await routeModelB(page, 'text', text.subarray(0, text.length - 1));
		await page.goto(`${baseUrl}/s/metadata-retry${modelBFragment}`);
		const open = page.getByRole('button', { name: 'Open', exact: true });
		await open.click();
		await page
			.getByRole('alert')
			.filter({
				hasText: 'This secret was opened but could not be decrypted. It cannot be opened again.'
			})
			.waitFor();
		assert.equal(await open.isEnabled(), false);
		await page.getByRole('link', { name: 'Create a new secret instead' }).waitFor();
		assert.equal(await page.getByRole('textbox', { name: 'Decrypted secret' }).count(), 0);
		assert.equal(await page.getByText(age.plaintext.text, { exact: false }).count(), 0);
	} finally {
		await page.close();
	}
});
