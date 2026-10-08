// biome-ignore-all lint/style/useNamingConvention: API fixture fields use the wire-format names.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const vectors = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/client-crypto-vectors.json', import.meta.url))
);
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const secretPath = '/api/secrets/metadata-retry';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

for (const model of ['a', 'b']) {
	for (const failure of ['network', 503, 429]) {
		test(`Model ${model.toUpperCase()} recovers from ${failure} without a reload`, async () => {
			const page = await browser.newPage();
			try {
				const vector = vectors[`text_model_${model}`];
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
							size_bytes: Buffer.byteLength(vector.plaintext),
							expires_at: '2099-01-01T00:00:00Z',
							...(model === 'a' ? { access: { kdf: vectors.access_proof.kdf } } : {})
						}
					});
				});
				await page.route(`**${secretPath}/open`, async (route) => {
					opens += 1;
					assert.deepEqual(
						route.request().postDataJSON(),
						model === 'a' ? { access_proof: vectors.access_proof.proof } : {}
					);
					await route.fulfill({
						json: {
							id: 'metadata-retry',
							kind: 'text',
							ciphertext: vector.ciphertext,
							nonce: vector.nonce,
							...(model === 'a' ? { kdf: vector.kdf } : {}),
							size_bytes: Buffer.byteLength(vector.plaintext),
							expires_at: '2099-01-01T00:00:00Z'
						}
					});
				});
				const fragment =
					model === 'b' ? `#key=${Buffer.from(vector.key, 'base64').toString('base64url')}` : '';
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
					await page.getByLabel('passphrase', { exact: true }).fill(vector.passphrase);
				} else {
					await page.getByText('This link opens without a passphrase.', { exact: false }).waitFor();
					assert.equal(await page.getByLabel('passphrase', { exact: true }).count(), 0);
				}
				await open.click();
				const secret = page.getByRole('textbox', { name: 'Decrypted secret' });
				await secret.waitFor();
				assert.equal(await secret.inputValue(), vector.plaintext);
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
			const fragment = `#key=${Buffer.from(vectors.text_model_b.key, 'base64').toString('base64url')}`;
			await page.goto(`${baseUrl}/s/metadata-retry${fragment}`);
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
