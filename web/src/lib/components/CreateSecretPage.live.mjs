// Runs against an isolated live API + MinIO, not mocked upload responses.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const passphrase = 'upload-browser-fixture';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

async function prepare(page, bytes) {
	page.setDefaultTimeout(15_000);
	await Promise.all([
		page.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
		page.goto(baseUrl)
	]);
	await page.getByRole('button', { name: 'File', exact: true }).click();
	await page.getByLabel('Add files to upload').setInputFiles({
		name: 'fixture.bin',
		mimeType: 'application/octet-stream',
		buffer: bytes
	});
	await page.getByLabel('Passphrase', { exact: true }).fill(passphrase);
}

for (const [name, size] of [
	['inline', 64],
	['large', 2 * 1024 * 1024]
]) {
	test(`${name} file round-trips; upload completion waits for finalize`, async () => {
		const page = await browser.newPage();
		const release = Promise.withResolvers();
		const original = Buffer.alloc(size, 123);
		try {
			await page.addInitScript(() => {
				window.uploadEvents = [];
				const send = XMLHttpRequest.prototype.send;
				XMLHttpRequest.prototype.send = function (body) {
					this.upload.addEventListener('progress', (event) => {
						window.uploadEvents.push({
							loaded: event.loaded,
							total: event.total,
							known: event.lengthComputable
						});
					});
					return send.call(this, body);
				};
			});
			if (name === 'large') {
				await page.route('**/api/secrets/*/finalize', async (route) => {
					await release.promise;
					await route.continue();
				});
			}
			await prepare(page, original);
			await page.getByRole('button', { name: 'Create link', exact: true }).click();
			if (name === 'large') {
				await page.getByRole('status').filter({ hasText: 'Finalizing link' }).waitFor();
				assert.equal(await page.getByLabel('Share URL').count(), 0);
				const events = await page.evaluate(() => window.uploadEvents);
				assert.ok(
					events.some(
						(event) => event.known && event.loaded === size + 16 && event.total === size + 16
					)
				);
				release.resolve();
			}
			await page.getByRole('heading', { name: 'Link created' }).waitFor();
			const url = await page.getByLabel('Share URL').inputValue();
			await page.goto(url);
			await page.getByLabel('passphrase', { exact: true }).fill(passphrase);
			await page.getByRole('button', { name: 'Open', exact: true }).click();
			const downloadReady = page.waitForEvent('download');
			await page.getByRole('link', { name: 'Download', exact: true }).click();
			const download = await downloadReady;
			assert.deepEqual(await readFile(await download.path()), original);
		} finally {
			release.resolve();
			await page.close();
		}
	});
}

test('cancelled upload never finalizes or publishes a share URL', async () => {
	const page = await browser.newPage();
	try {
		const session = await page.context().newCDPSession(page);
		await session.send('Network.enable');
		await session.send('Network.emulateNetworkConditions', {
			offline: false,
			latency: 20,
			downloadThroughput: 1024 * 1024,
			uploadThroughput: 128 * 1024
		});
		let finalizes = 0;
		page.on('request', (request) => {
			if (request.url().endsWith('/finalize')) {
				finalizes += 1;
			}
		});
		await prepare(page, Buffer.alloc(2 * 1024 * 1024, 42));
		await page.getByRole('button', { name: 'Create link', exact: true }).click();
		await page.getByRole('progressbar', { name: 'Encrypted file upload progress' }).waitFor();
		await page.getByRole('button', { name: 'Cancel', exact: true }).click();
		await page.getByRole('status').filter({ hasText: 'Upload cancelled.' }).waitFor();
		assert.equal(await page.getByLabel('Share URL').count(), 0);
		assert.equal(await page.getByRole('progressbar').count(), 0);
		assert.equal(
			await page.getByRole('button', { name: 'Create link', exact: true }).isEnabled(),
			true
		);
		assert.equal(finalizes, 0);
	} finally {
		await page.close();
	}
});
