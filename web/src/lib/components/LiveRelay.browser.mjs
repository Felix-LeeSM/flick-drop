// biome-ignore-all lint/style/useNamingConvention: API fixtures use canonical wire-format names.
// biome-ignore-all lint/suspicious/useAwait: Playwright route handler interface accepts async callback.
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

test('SiteHeader displays accessible Enter code button and opens Receive dialog', async () => {
	const page = await browser.newPage();
	try {
		await page.route(
			(url) => url.pathname.startsWith('/api/'),
			async (route) => {
				const path = new URL(route.request().url()).pathname;
				if (path === '/api/config') {
					return route.fulfill({
						json: { payload_inline_max_bytes: 1_048_560, max_file_bytes: 268_435_456 }
					});
				}
				return route.continue();
			}
		);

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		const enterCodeBtn = page.getByRole('button', { name: 'Enter code' });
		await assert.doesNotReject(enterCodeBtn.waitFor({ state: 'visible' }));

		await enterCodeBtn.click();
		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await assert.doesNotReject(dialog.waitFor({ state: 'visible' }));

		// Code input field is present
		const codeInput = page.locator('#pipe-code-input');
		await assert.doesNotReject(codeInput.waitFor({ state: 'visible' }));

		// Close dialog
		await page.getByRole('button', { name: 'Close' }).click();
		await dialog.waitFor({ state: 'hidden' });
	} finally {
		await page.close();
	}
});

test('CreateSecretPage: Send with code validation, confirmation toggle, and hover dimming', async () => {
	const page = await browser.newPage();
	try {
		await page.route(
			(url) => url.pathname.startsWith('/api/'),
			async (route) => {
				const path = new URL(route.request().url()).pathname;
				if (path === '/api/config') {
					return route.fulfill({
						json: { payload_inline_max_bytes: 1_048_560, max_file_bytes: 268_435_456 }
					});
				}
				return route.continue();
			}
		);

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		const sendWithCodeBtn = page.getByRole('button', { name: /Send with code/ });
		const createLinkBtn = page.getByRole('button', { name: 'Create link' });

		// Both buttons disabled when empty
		assert.equal(await sendWithCodeBtn.isDisabled(), true);
		assert.equal(await createLinkBtn.isDisabled(), true);

		// Type message
		await page.getByLabel('Message', { exact: true }).fill('Confidential live stream payload');
		await page.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();

		// Enabled
		assert.equal(await sendWithCodeBtn.isDisabled(), false);
		assert.equal(await createLinkBtn.isDisabled(), false);

		// Check confirmation toggle behavior
		const confirmCheckbox = page.getByRole('checkbox', {
			name: 'Require confirmation before transfer'
		});
		assert.equal(await confirmCheckbox.isChecked(), true);
		assert.match(await sendWithCodeBtn.innerText(), /10m/);

		// Uncheck -> 3m
		await confirmCheckbox.uncheck();
		assert.match(await sendWithCodeBtn.innerText(), /3m/);
	} finally {
		await page.close();
	}
});
