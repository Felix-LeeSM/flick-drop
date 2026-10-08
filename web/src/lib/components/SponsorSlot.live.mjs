import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const enabled = process.env.FLICK_SPONSOR_TEST_ENABLED === '1';
const destination = 'https://sponsor.example.test/';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

for (const width of [375, 1280]) {
	test(`guide sponsor is isolated and usable at ${width}px (enabled=${enabled})`, async () => {
		const context = await browser.newContext({ viewport: { width, height: 900 } });
		const page = await context.newPage();
		const remoteRequests = [];
		context.on('request', (request) => {
			if (new URL(request.url()).origin !== new URL(baseUrl).origin) {
				remoteRequests.push(request);
			}
		});
		try {
			await page.goto(`${baseUrl}/guides/password-sharing/?synthetic=context#manage=not-a-token`);
			const slot = page.getByRole('complementary', { name: 'Sponsored' });
			assert.equal(await slot.count(), enabled ? 1 : 0);
			assert.deepEqual(remoteRequests, []);
			assert.equal(
				await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
			if (enabled) {
				const link = slot.getByRole('link', { name: 'Synthetic sponsor (opens in a new tab)' });
				assert.equal(await link.getAttribute('href'), destination);
				await link.focus();
				assert.equal(await link.evaluate((element) => document.activeElement === element), true);
				await context.route(destination, (route) =>
					route.fulfill({ body: 'Synthetic destination' })
				);
				const popupPromise = page.waitForEvent('popup');
				await page.keyboard.press('Enter');
				const popup = await popupPromise;
				await popup.waitForLoadState();
				assert.equal(await popup.evaluate(() => window.opener), null);
				assert.equal(remoteRequests.length, 1);
				assert.equal(remoteRequests[0].url(), destination);
				assert.equal(remoteRequests[0].headers().referer, undefined);
				await popup.close();
			}
			for (const path of ['/', '/s/synthetic', '/m/synthetic', '/r/synthetic/receive']) {
				await page.goto(`${baseUrl}${path}`);
				assert.equal(await page.getByRole('complementary', { name: 'Sponsored' }).count(), 0);
			}
		} finally {
			await context.close();
		}
	});
}
