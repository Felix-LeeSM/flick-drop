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

// The header carries no Enter code action (prototype round R1): the receive
// dialog opens from the code tab's "Enter it here" link.
async function openReceiveDialog(page) {
	await page
		.getByRole('group', { name: 'Delivery method' })
		.getByRole('button', { name: 'Send with code' })
		.click();
	await page.getByRole('button', { name: 'Enter it here' }).click();
}

test('The code tab Enter-it-here button opens the Receive dialog', async () => {
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

		await openReceiveDialog(page);
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

test('Receive dialog mirrors typed digits in six key caps and moves to step 2 at 320px', async () => {
	const page = await browser.newPage({ viewport: { width: 320, height: 700 } });
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
				// Never answered, so the dialog stays on step 2 waiting for the sender.
				if (path === '/api/pipe/receive') {
					return;
				}
				return route.continue();
			}
		);
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await openReceiveDialog(page);
		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await dialog.getByText('STEP 1 OF 2', { exact: true }).waitFor();
		await dialog.getByRole('heading', { name: 'Connect to sender' }).waitFor();

		// The dialog moves focus to Close when it opens; typing before that lands would lose keys.
		await page.waitForFunction(
			() => document.activeElement?.getAttribute('aria-label') === 'Close'
		);
		const input = page.locator('#pipe-code-input');
		await input.focus();
		await input.pressSequentially('74x1');
		assert.equal(await input.inputValue(), '741');
		// The invisible field holds only digits, so Backspace after a stray letter removes a digit.
		await input.press('x');
		await input.press('Backspace');
		assert.equal(await input.inputValue(), '74');
		await input.press('1');
		// Measure after the dialog's zoom-in and the caps' lift settle.
		await page.waitForFunction(
			() => document.querySelector('[role="dialog"]').getAnimations({ subtree: true }).length === 0
		);
		// The key caps are decoration over the one transparent input; the next empty cap shows focus.
		const caps = await input.evaluate((field) => {
			const area = field.getBoundingClientRect();
			const content = field.closest('[role="dialog"]').getBoundingClientRect();
			return [...field.parentElement.querySelectorAll('span.rounded-lg')].map((cap) => {
				const box = cap.getBoundingClientRect();
				return {
					text: cap.textContent.trim(),
					hidden: cap.getAttribute('aria-hidden'),
					ring: getComputedStyle(cap).boxShadow !== 'none',
					covered: box.left >= area.left && box.right <= area.right,
					inside: box.left >= content.left && box.right <= content.right
				};
			});
		});
		assert.deepEqual(
			caps.map((cap) => cap.text),
			['7', '4', '1', '0', '0', '0']
		);
		assert.deepEqual(
			caps.map((cap) => cap.ring),
			[false, false, false, true, false, false]
		);
		assert.ok(caps.every((cap) => cap.hidden === 'true' && cap.covered && cap.inside));
		// Forced colors drop the ring, so the focused cap also carries an outline that stays
		// transparent until forced colors repaint it.
		const outline = () =>
			input.evaluate((field) => {
				const style = getComputedStyle(field.parentElement.querySelectorAll('span.rounded-lg')[3]);
				return [style.outlineStyle, style.outlineWidth, style.outlineColor];
			});
		assert.deepEqual(await outline(), ['solid', '2px', 'rgba(0, 0, 0, 0)']);
		await page.emulateMedia({ forcedColors: 'active' });
		const [style, width, color] = await outline();
		assert.deepEqual([style, width], ['solid', '2px']);
		assert.notEqual(color, 'rgba(0, 0, 0, 0)');
		await page.emulateMedia({ forcedColors: 'none' });

		const toggle = dialog.getByRole('button', { name: 'Has an additional passphrase?' });
		assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
		await toggle.click();
		await dialog.getByLabel('Additional passphrase', { exact: true }).waitFor();
		assert.equal(
			await dialog
				.getByRole('button', { name: 'Hide additional passphrase' })
				.getAttribute('aria-expanded'),
			'true'
		);

		// The sender's screen shows the code as "741-928"; pasting it whole still connects.
		await input.fill('741-928');
		await dialog.getByText('STEP 2 OF 2', { exact: true }).waitFor();
		await dialog.getByText('Pipe: 741-928', { exact: true }).waitFor();
		await dialog.getByRole('heading', { name: "Check sender's screen" }).waitFor();
		assert.match(
			await dialog.getByText('Confirmation Code').locator('xpath=..').innerText(),
			/\d{3}\s*$/
		);
		assert.equal(
			await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
			true
		);
	} finally {
		await page.close();
	}
});

test('CreateSecretPage: tab CTAs gate on payload and protection switches ship OFF', async () => {
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

		const tabs = page.getByRole('group', { name: 'Delivery method' });
		const createSubmit = page.locator('form button[type="submit"]');

		// The link tab's CTA stays disabled until there is something to send.
		assert.equal(await createSubmit.isDisabled(), true);

		// Both protection switches ship OFF — each tab owns one.
		const passphraseSwitch = page.getByRole('switch', {
			name: 'Protect with an additional passphrase'
		});
		assert.equal(await passphraseSwitch.getAttribute('data-state'), 'unchecked');

		await tabs.getByRole('button', { name: 'Send with code' }).click();
		const startPipe = page.getByRole('button', { name: 'Start live transfer' });
		const confirmSwitch = page.getByRole('switch', {
			name: 'Require confirmation before transfer'
		});
		assert.equal(await confirmSwitch.getAttribute('data-state'), 'unchecked');
		assert.equal(await startPipe.isDisabled(), true);

		// Typing a message enables the tab's CTA.
		await page.getByLabel('Message', { exact: true }).fill('Confidential live stream payload');
		assert.equal(await startPipe.isDisabled(), false);

		// The confirmation switch alone does not block the CTA.
		await confirmSwitch.click();
		assert.equal(await confirmSwitch.getAttribute('data-state'), 'checked');
		assert.equal(await startPipe.isDisabled(), false);

		// Back on the link tab, the passphrase switch does block the CTA until a
		// passphrase exists.
		await tabs.getByRole('button', { name: 'Create link' }).click();
		assert.equal(await createSubmit.isDisabled(), false);
		await passphraseSwitch.click();
		assert.equal(await passphraseSwitch.getAttribute('data-state'), 'checked');
		assert.equal(await createSubmit.isDisabled(), true);
		await page.getByLabel('Additional passphrase', { exact: true }).fill('extra');
		assert.equal(await createSubmit.isDisabled(), false);
	} finally {
		await page.close();
	}
});

test('QR scan direct hash navigation: navigating to /#code=123456 auto-opens Receive modal', async () => {
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

		await page.goto(`${baseUrl}/#code=123456`);
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

		await openReceiveDialog(page);
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
		await senderPage
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Send with code' })
			.click();
		// The 2-step scenario turns the opt-in confirmation on before starting.
		await senderPage.getByRole('switch', { name: 'Require confirmation before transfer' }).click();
		await senderPage.getByRole('button', { name: 'Start live transfer' }).click();

		// Waiting panel displays 6-digit code
		const codeCard = senderPage.getByLabel('Transfer code 741-928');
		await assert.doesNotReject(codeCard.waitFor({ state: 'visible' }));

		// Step 2: Receiver connects with code 741928
		await receiverPage.goto(`${baseUrl}/`);
		await receiverPage.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await openReceiveDialog(receiverPage);
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
