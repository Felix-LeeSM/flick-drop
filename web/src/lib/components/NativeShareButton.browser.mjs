// biome-ignore-all lint/style/useNamingConvention: API fixture fields use the wire-format names.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

for (const [model, support, outcome] of [
	['a', 'supported', 'success'],
	['b', 'supported', 'success'],
	['b', 'supported', 'cancelled'],
	['b', 'supported', 'rejected'],
	['b', 'without-can-share', 'success'],
	['b', 'unsupported', 'unused'],
	['b', 'cannot-share', 'unused']
]) {
	test(`Model ${model.toUpperCase()} ${support} ${outcome} shares only the recipient link`, async () => {
		const page = await browser.newPage({ viewport: { width: 375, height: 812 }, hasTouch: true });
		try {
			let creates = 0;
			let opens = 0;
			await page.addInitScript(
				({ support, outcome }) => {
					window.shareCalls = [];
					window.copiedLinks = [];
					Object.defineProperty(navigator, 'share', {
						configurable: true,
						value:
							support === 'unsupported'
								? undefined
								: (data) => {
										window.shareCalls.push({ data, activated: navigator.userActivation.isActive });
										return new Promise((resolve, reject) => {
											window.finishShare = () => {
												if (outcome === 'success') {
													resolve();
												} else {
													reject(
														new DOMException(
															'private error details must not be displayed',
															outcome === 'cancelled' ? 'AbortError' : 'NotAllowedError'
														)
													);
												}
											};
										});
									}
					});
					Object.defineProperty(navigator, 'canShare', {
						configurable: true,
						value: support === 'without-can-share' ? undefined : () => support !== 'cannot-share'
					});
					Object.defineProperty(navigator, 'clipboard', {
						configurable: true,
						value: { writeText: async (value) => window.copiedLinks.push(value) }
					});
				},
				{ support, outcome }
			);
			await page.route('**/api/config', (route) => route.fulfill({ json: {} }));
			await page.route('**/api/secrets', async (route) => {
				creates += 1;
				await route.fulfill({
					json: { id: 'native-share', expires_at: '2099-01-01T00:00:00Z' }
				});
			});
			await page.route('**/api/secrets/*/open', async (route) => {
				opens += 1;
				await route.abort();
			});
			const hydrated = page.waitForResponse('**/api/config');
			await page.goto(baseUrl);
			await hydrated;
			await page.getByLabel('Message', { exact: true }).fill('private synthetic content');
			if (model === 'a') {
				await page.getByLabel('Passphrase', { exact: true }).fill('private synthetic passphrase');
			} else {
				await page.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();
			}
			await page.getByRole('button', { name: 'Create link', exact: true }).click();
			await page.getByRole('heading', { name: 'Link created' }).waitFor();
			const recipientUrl = await page.getByRole('textbox', { name: 'Share URL' }).inputValue();
			assert.equal(new URL(recipientUrl).pathname, '/s/native-share');
			assert.equal(new URL(recipientUrl).search, '');
			if (model === 'a') {
				assert.equal(new URL(recipientUrl).hash, '');
				await page.getByText('Send the passphrase separately.', { exact: true }).waitFor();
			} else {
				assert.match(new URL(recipientUrl).hash, /^#key=[A-Za-z0-9_-]{43}$/);
			}
			// A future management route must share the provided link, never its own URL.
			await page.evaluate(() => history.replaceState(null, '', '#manage=private-management-token'));
			const share = page.getByRole('button', { name: 'Share link', exact: true });
			if (outcome === 'unused') {
				assert.equal(await share.count(), 0);
				assert.deepEqual(await page.evaluate(() => window.shareCalls), []);
			} else {
				await share.waitFor();
				const box = await share.boundingBox();
				assert.ok(box && box.x >= 0 && box.x + box.width <= 375 && box.height >= 44);
				if (model === 'a') {
					await share.focus();
					await page.keyboard.press('Enter');
				} else {
					await share.tap();
				}
				await page.waitForFunction(() => window.shareCalls.length === 1);
				assert.equal(await share.isEnabled(), false);
				await share.dispatchEvent('click');
				assert.deepEqual(await page.evaluate(() => window.shareCalls), [
					{ data: { title: 'Flick', url: recipientUrl }, activated: true }
				]);
				await page.evaluate(() => window.finishShare());
				await page.waitForFunction(() => !document.querySelector('button:disabled'));
				if (outcome === 'rejected') {
					await page.getByRole('alert').filter({ hasText: 'Could not share.' }).waitFor();
				} else {
					assert.equal(await page.getByRole('alert').count(), 0);
				}
				assert.equal(await page.getByText('private error details', { exact: false }).count(), 0);
			}
			await page.getByRole('button', { name: 'Copy to clipboard', exact: true }).click();
			assert.deepEqual(await page.evaluate(() => window.copiedLinks), [recipientUrl]);
			await page.getByRole('button', { name: 'Show QR', exact: true }).click();
			const qrDialog = page.getByRole('dialog');
			await qrDialog.waitFor();
			assert.equal(
				await qrDialog.getByRole('textbox', { name: 'Share URL' }).inputValue(),
				recipientUrl
			);
			assert.equal(creates, 1);
			assert.equal(opens, 0);
		} finally {
			await page.close();
		}
	});
}
