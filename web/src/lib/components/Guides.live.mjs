import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

// Serve the built app with nginx and a real same-origin API for this suite.
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const paths = ['/guides/password-sharing/', '/guides/temporary-file-sharing/'];
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

for (const width of [375, 1280]) {
	test(`guides render accessible public content at ${width}px without remote requests`, async () => {
		const page = await browser.newPage({ viewport: { width, height: 900 } });
		const remote = [];
		page.on('request', (request) => {
			if (new URL(request.url()).origin !== new URL(baseUrl).origin) {
				remote.push(request.url());
			}
		});
		try {
			for (const path of paths) {
				const response = await page.goto(`${baseUrl}${path}?source=synthetic#unused`);
				assert.equal(response.status(), 200);
				assert.equal(response.headers()['x-robots-tag'], undefined);
				assert.match(await page.title(), / - Flick$/);
				assert.ok(
					(await page.locator('meta[name="description"]').getAttribute('content')).length > 80
				);
				assert.equal(
					await page.locator('link[rel="canonical"]').evaluate((link) => link.href),
					`${baseUrl}${path}`
				);
				assert.ok((await page.locator('article').innerText()).length > 1500);
				assert.equal(
					await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
					true
				);
				await page.getByRole('link', { name: 'Create a one-time link' }).focus();
				assert.equal(
					await page.evaluate(() => document.activeElement.textContent.trim()),
					'Create a one-time link'
				);
				assert.equal(
					await page.getByRole('navigation', { name: 'Sharing guides' }).getByRole('link').count(),
					3
				);
			}
			assert.deepEqual(remote, []);
			if (process.env.FLICK_GUIDE_SCREENSHOT) {
				await page.screenshot({
					path: `${process.env.FLICK_GUIDE_SCREENSHOT}-${width}.png`,
					fullPage: true
				});
			}
		} finally {
			await page.close();
		}
	});
}

test('private SPA routes are noindex before hydration and excluded from robots', async () => {
	for (const path of ['/s/synthetic', '/m/synthetic', '/r/synthetic/receive']) {
		const response = await fetch(`${baseUrl}${path}`);
		assert.equal(response.headers.get('x-robots-tag'), 'noindex, nofollow');
		assert.equal((await response.text()).includes('rel="canonical"'), false);
	}
	const robots = await (await fetch(`${baseUrl}/robots.txt`)).text();
	for (const prefix of ['/s/', '/m/', '/r/', '/api/']) {
		assert.ok(robots.includes(`Disallow: ${prefix}`));
	}
	assert.equal(robots.includes('Sitemap:'), false);
});

test('guide CTA reaches a real synthetic create and one-time open', async () => {
	const page = await browser.newPage();
	try {
		await page.goto(`${baseUrl}${paths[0]}`);
		await Promise.all([
			page.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
			page.getByRole('link', { name: 'Create a one-time link' }).click()
		]);
		await page.getByLabel('Message', { exact: true }).fill('Synthetic guide handoff');
		await page.getByLabel('Passphrase', { exact: true }).fill('synthetic-guide-passphrase');
		await page.getByRole('button', { name: 'Create link', exact: true }).click();
		await page.getByRole('heading', { name: 'Link created' }).waitFor();
		const url = await page.getByLabel('Share URL').inputValue();
		await page.goto(url);
		await page.getByLabel('passphrase', { exact: true }).fill('synthetic-guide-passphrase');
		await page.getByRole('button', { name: 'Open', exact: true }).click();
		await page.getByLabel('Decrypted secret').waitFor();
		assert.equal(await page.getByLabel('Decrypted secret').inputValue(), 'Synthetic guide handoff');
		await page.reload();
		await page.getByText('This secret is no longer available.', { exact: true }).waitFor();
	} finally {
		await page.close();
	}
});
