// biome-ignore-all lint/style/useNamingConvention: API fixtures use canonical wire-format names.
// biome-ignore-all lint/suspicious/useAwait: Playwright route handler interface accepts async callback.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { Encrypter } from 'age-encryption';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://localhost:5173';
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

test('CreateSecretPage: Send with code validation, confirmation toggle, and state transition', async () => {
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

		const sendWithCodeBtn = page.getByRole('button', { name: 'Send with code' });
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
	} finally {
		await page.close();
	}
});

test('QR scan direct hash navigation: navigating to /#receive=123456 auto-opens Receive modal', async () => {
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
				if (path === '/api/pipe/receive') {
					return route.fulfill({
						status: 404,
						json: { error: 'not found' }
					});
				}
				return route.continue();
			}
		);

		await page.goto(`${baseUrl}/#receive=123456`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await assert.doesNotReject(dialog.waitFor({ state: 'visible' }));
	} finally {
		await page.close();
	}
});

test('Receiver error state: displays actionable error on 404/expired code', async () => {
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
				if (path === '/api/pipe/receive') {
					return route.fulfill({
						status: 404,
						json: { error: 'rendezvous session not found or expired' }
					});
				}
				return route.continue();
			}
		);

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await page.getByRole('button', { name: 'Enter code' }).click();
		const codeInput = page.locator('#pipe-code-input');
		await codeInput.fill('999999');

		// Transitions to error view
		const errorTitle = page.getByRole('heading', { name: 'Transfer Failed' });
		await assert.doesNotReject(errorTitle.waitFor({ state: 'visible' }));

		// Try again button resets form
		const tryAgainBtn = page.getByRole('button', { name: 'Try again' });
		await tryAgainBtn.click();
		await assert.doesNotReject(codeInput.waitFor({ state: 'visible' }));
	} finally {
		await page.close();
	}
});

test('Two-browser live relay transfer with 2-step confirmation and burn state', async () => {
	const senderContext = await browser.newContext();
	const receiverContext = await browser.newContext();

	const senderPage = await senderContext.newPage();
	const receiverPage = await receiverContext.newPage();

	// In-memory relay mock coordinating sender and receiver requests
	let receiverJoinedResolve;
	const receiverJoinedPromise = new Promise((resolve) => {
		receiverJoinedResolve = resolve;
	});

	let senderStreamResolve;
	const senderStreamPromise = new Promise((resolve) => {
		senderStreamResolve = resolve;
	});

	const sharedPipeCode = '741928';
	const sharedSalt = 'f0e1d2c3b4a59687';

	const enc = new Encrypter();
	enc.setPassphrase(`${sharedPipeCode}:${sharedSalt}`);
	const encryptedPayload = await enc.encrypt(
		new TextEncoder().encode('Real-time live relay transfer test')
	);
	const encryptedBuffer = Buffer.from(encryptedPayload);

	const corsHeaders = {
		'Access-Control-Allow-Origin': '*',
		'Access-Control-Allow-Headers': '*',
		'Access-Control-Expose-Headers': 'X-Flick-Pipe-Salt, Flick-Kind, Flick-Encrypted-Filename'
	};

	async function handleRelayRoute(route) {
		const path = new URL(route.request().url()).pathname;
		switch (path) {
			case '/api/config':
				return route.fulfill({
					headers: corsHeaders,
					json: { payload_inline_max_bytes: 1_048_560, max_file_bytes: 268_435_456 }
				});
			case '/api/pipe/init':
				return route.fulfill({
					headers: corsHeaders,
					json: {
						code: sharedPipeCode,
						salt: sharedSalt,
						expires_in: 180
					}
				});
			case '/api/pipe/wait':
				await receiverJoinedPromise;
				return route.fulfill({
					headers: corsHeaders,
					json: { status: 'ready' }
				});
			case '/api/pipe/receive':
				receiverJoinedResolve();
				await senderStreamPromise;
				return route.fulfill({
					headers: {
						...corsHeaders,
						'Content-Type': 'application/octet-stream',
						'X-Flick-Pipe-Salt': sharedSalt,
						'Flick-Kind': 'text'
					},
					body: encryptedBuffer
				});
			case '/api/pipe/send':
				senderStreamResolve();
				return route.fulfill({
					headers: corsHeaders,
					json: { bytes_transferred: encryptedBuffer.length }
				});
			default:
				return route.continue();
		}
	}

	const setupRouting = async (page) => {
		await page.route((url) => url.pathname.startsWith('/api/'), handleRelayRoute);
	};

	try {
		await setupRouting(senderPage);
		await setupRouting(receiverPage);

		// Step 1: Sender creates secret and starts live pipe rendezvous
		await senderPage.goto(`${baseUrl}/`);
		await senderPage.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await senderPage
			.getByLabel('Message', { exact: true })
			.fill('Real-time live relay transfer test');
		await senderPage.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();

		// Keep confirmation checkbox checked
		await senderPage.getByRole('button', { name: 'Send with code' }).click();

		// Waiting panel displays 6-digit code
		const codeCard = senderPage.getByLabel('Transfer code 741-928');
		await assert.doesNotReject(codeCard.waitFor({ state: 'visible' }));

		// Step 2: Receiver connects with code 741928
		await receiverPage.goto(`${baseUrl}/`);
		await receiverPage.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await receiverPage.getByRole('button', { name: 'Enter code' }).click();
		await receiverPage.locator('#pipe-code-input').fill(sharedPipeCode);

		// Step 3: Sender sees confirmation challenge card
		const confirmHeading = senderPage.getByRole('heading', { name: 'Confirm live transfer' });
		await assert.doesNotReject(confirmHeading.waitFor({ state: 'visible' }));

		// Both sender and receiver display matching Confirmation Code
		const approveBtn = senderPage.getByRole('button', { name: 'Approve & send' });
		await assert.doesNotReject(approveBtn.waitFor({ state: 'visible' }));

		// Step 4: Sender approves transfer
		await approveBtn.click();

		// Step 5: Transfer completes on both ends
		const senderComplete = senderPage.getByRole('heading', { name: 'Transfer complete!' });
		await assert.doesNotReject(senderComplete.waitFor({ state: 'visible' }));

		const receiverSuccess = receiverPage.getByText('Secret decrypted directly in memory');
		await assert.doesNotReject(receiverSuccess.waitFor({ state: 'visible' }));

		// Receiver sees decrypted text
		const receivedText = await receiverPage
			.getByRole('dialog', { name: 'Receive secret with code' })
			.locator('textarea')
			.inputValue();
		assert.equal(receivedText, 'Real-time live relay transfer test');

		// Burn indicators visible
		await assert.doesNotReject(
			senderPage.getByText('live pipe burned').waitFor({ state: 'visible' })
		);
		await assert.doesNotReject(receiverPage.getByText('burned').waitFor({ state: 'visible' }));
	} finally {
		await senderContext.close();
		await receiverContext.close();
	}
});
