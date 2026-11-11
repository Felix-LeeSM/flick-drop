// biome-ignore-all lint/style/useNamingConvention: API fixtures use canonical wire-format names.
// biome-ignore-all lint/suspicious/useAwait: Playwright route handler interface accepts async callback.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://localhost:5173';
let browser;

before(async () => {
	browser = await chromium.launch();
});

after(async () => {
	await browser?.close();
});

const configJson = { payload_inline_max_bytes: 1_048_560, max_file_bytes: 268_435_456 };
const corsHeaders = {
	'Access-Control-Allow-Origin': '*',
	'Access-Control-Allow-Headers': '*',
	'Access-Control-Expose-Headers': 'X-Flick-Pipe-Salt, Flick-Kind, Flick-Encrypted-Filename'
};

// The header carries no Enter code action (prototype round R1): the receive
// dialog opens from the code tab's "Enter it here" link.
async function openReceiveDialog(page) {
	await page
		.getByRole('group', { name: 'Delivery method' })
		.getByRole('button', { name: 'Send with code' })
		.click();
	await page.getByRole('button', { name: 'Enter it here' }).click();
}

// Routes every /api/ call through handle(path, route); /api/config and CORS
// preflights are answered here.
async function routeApi(page, handle) {
	await page.route(
		(url) => url.pathname.startsWith('/api/'),
		async (route) => {
			if (route.request().method() === 'OPTIONS') {
				return route.fulfill({ status: 204, headers: corsHeaders });
			}
			const path = new URL(route.request().url()).pathname;
			if (path === '/api/config') {
				return route.fulfill({ headers: corsHeaders, json: configJson });
			}
			return handle(path, route);
		}
	);
}

function errorJson(code) {
	return { error: { code, message: code } };
}

test('The code tab Enter-it-here button opens the Receive dialog', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (_path, route) => route.continue());

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

test('Receive dialog mirrors typed digits in six key caps and shows its own number at 320px', async () => {
	const page = await browser.newPage({ viewport: { width: 320, height: 700 } });
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/join') {
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						connection_id: 'conn-1',
						require_confirmation: true,
						verify_code: '042',
						device: 'Chrome on Linux',
						expires_in: 170
					}
				});
			}
			// Never answered, so the dialog stays on step 2 waiting for the sender's pick.
			if (path === '/api/pipe/receive') {
				return;
			}
			return route.continue();
		});
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
		await dialog.getByRole('heading', { name: 'Read this to the sender' }).waitFor();
		await dialog.getByText('Pipe: 741-928', { exact: true }).waitFor();
		// The server's per-connection number, not anything derived from the 6-digit code.
		await dialog.getByRole('img', { name: 'Your number 0 4 2' }).waitFor();
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
		await routeApi(page, (_path, route) => route.continue());

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
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/join') {
				return route.fulfill({ status: 404, headers: corsHeaders, json: errorJson('not_found') });
			}
			return route.continue();
		});

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
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/join') {
				return route.fulfill({ status: 404, headers: corsHeaders, json: errorJson('not_found') });
			}
			return route.continue();
		});

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await openReceiveDialog(page);
		const codeInput = page.locator('#pipe-code-input');
		await codeInput.fill('999999');

		// Transitions to error view
		const errorTitle = page.getByRole('heading', { name: 'Transfer Failed' });
		await assert.doesNotReject(errorTitle.waitFor({ state: 'visible' }));
		await page.getByText('Invalid or expired 6-digit code.').waitFor();

		// Try again button resets form
		const tryAgainBtn = page.getByRole('button', { name: 'Try again' });
		await tryAgainBtn.click();
		await assert.doesNotReject(codeInput.waitFor({ state: 'visible' }));
	} finally {
		await page.close();
	}
});

test('Receiver room full: an 11th device is told to ask for a new code', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/join') {
				return route.fulfill({ status: 409, headers: corsHeaders, json: errorJson('room_full') });
			}
			return route.continue();
		});
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await openReceiveDialog(page);
		await page.locator('#pipe-code-input').fill('741928');

		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await dialog.getByRole('heading', { name: 'Too many devices are trying to connect' }).waitFor();
		await dialog.getByText('Ask the sender for a new code.').waitFor();
		await dialog.getByRole('button', { name: 'Enter a new code' }).waitFor();
	} finally {
		await page.close();
	}
});

test('Sender waiting list: three rows show, the rest fold behind "Show N more"', async () => {
	const page = await browser.newPage();
	const devices = [
		'Chrome on Windows',
		'Safari on iPhone',
		'Firefox on Android',
		'Edge on Windows',
		'Samsung Internet on Android'
	];
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/init') {
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						code: '741928',
						salt: 'f0e1d2c3b4a59687',
						expires_in: 180,
						sender_token: 'tok',
						require_confirmation: true
					}
				});
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				if (since === '1') {
					return; // unchanged: hold the long-poll
				}
				return route.fulfill({
					headers: corsHeaders,
					json: {
						status: 'waiting',
						version: 1,
						connections: devices.map((device, i) => ({
							connection_id: `c${i + 1}`,
							index: i + 1,
							verify_code: String(101 + i * 111).padStart(3, '0'),
							device,
							connected_seconds: 12
						}))
					}
				});
			}
			return route.continue();
		});

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await page.getByLabel('Message', { exact: true }).fill('payload');
		await page
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Send with code' })
			.click();
		await page.getByRole('switch', { name: 'Require confirmation before transfer' }).click();
		await page.getByRole('button', { name: 'Start live transfer' }).click();

		// The 6-digit code stays on screen above the list.
		await page.getByLabel('Transfer code 741-928').waitFor();
		const list = page.getByRole('list', { name: 'Waiting devices' });
		await page.getByText('5 of 10 devices waiting').waitFor();
		await list.getByRole('listitem').first().waitFor();
		assert.equal(await list.getByRole('listitem').count(), 3);

		const more = page.getByRole('button', { name: 'Show 2 more' });
		assert.equal(await more.getAttribute('aria-expanded'), 'false');
		await more.click();
		assert.equal(await list.getByRole('listitem').count(), 5);
		await list.getByText('Samsung Internet on Android').waitFor();
		const fewer = page.getByRole('button', { name: 'Show fewer' });
		assert.equal(await fewer.getAttribute('aria-expanded'), 'true');
		await fewer.click();
		assert.equal(await list.getByRole('listitem').count(), 3);

		// Every row has a named Deny icon button and Send button, and the time is never cut off.
		await page.getByRole('button', { name: 'Deny connection 1' }).waitFor();
		await page.getByRole('button', { name: 'Send to connection 1, number 101' }).waitFor();
		const timeClipped = await list
			.getByText(/^\d+s ago$/)
			.first()
			.evaluate((node) => node.scrollWidth > node.clientWidth);
		assert.equal(timeClipped, false);
	} finally {
		await page.close();
	}
});

// An in-memory stand-in for the /api/pipe waiting room shared by every page in
// the test: it hands out per-connection numbers, long-polls the sender, and
// forwards the sender's real ciphertext to the chosen receiver only.
function createRelayMock() {
	const salt = 'f0e1d2c3b4a59687';
	const numbers = ['150', '271', '319', '428', '563'];
	const devices = ['Chrome on Windows', 'Safari on iPhone', 'Firefox on Android'];
	const connections = new Map();
	let version = 1;
	let changed = Promise.withResolvers();
	const sent = Promise.withResolvers();
	const joinCalls = new Map();

	function bump() {
		version += 1;
		changed.resolve();
		changed = Promise.withResolvers();
	}

	async function handle(page, path, route) {
		const request = route.request();
		const headers = request.headers();
		switch (path) {
			case '/api/pipe/init':
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						code: '741928',
						salt,
						expires_in: 180,
						sender_token: 'tok',
						require_confirmation: true
					}
				});
			case '/api/pipe/join': {
				joinCalls.set(page, (joinCalls.get(page) ?? 0) + 1);
				const index = connections.size + 1;
				const id = `conn-${index}`;
				const outcome = Promise.withResolvers();
				connections.set(id, {
					index,
					number: numbers[index - 1],
					device: devices[(index - 1) % devices.length],
					outcome,
					decided: false
				});
				bump();
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						connection_id: id,
						require_confirmation: true,
						verify_code: numbers[index - 1],
						device: devices[(index - 1) % devices.length],
						expires_in: 170
					}
				});
			}
			case '/api/pipe/wait': {
				const since = Number(new URL(request.url()).searchParams.get('since'));
				if (since === version) {
					await Promise.race([changed.promise, new Promise((r) => setTimeout(r, 1500))]);
				}
				const pending = [...connections.entries()].filter(([, c]) => !c.decided);
				return route.fulfill({
					headers: corsHeaders,
					json: {
						status: 'waiting',
						version,
						connections: pending.map(([id, c]) => ({
							connection_id: id,
							index: c.index,
							verify_code: c.number,
							device: c.device,
							connected_seconds: 1
						}))
					}
				});
			}
			case '/api/pipe/select':
			case '/api/pipe/decline': {
				const chosen = headers['x-flick-pipe-connection'];
				for (const [id, c] of connections) {
					if (c.decided) {
						continue;
					}
					if (id === chosen) {
						c.decided = true;
						c.outcome.resolve(path === '/api/pipe/select' ? 'selected' : 'declined');
					} else if (path === '/api/pipe/select') {
						c.decided = true;
						c.outcome.resolve('not_selected');
					}
				}
				bump();
				return route.fulfill({ headers: corsHeaders, json: { status: 'ok' } });
			}
			case '/api/pipe/receive': {
				const c = connections.get(headers['x-flick-pipe-connection']);
				const outcome = await c.outcome.promise;
				if (outcome !== 'selected') {
					return route.fulfill({
						status: outcome === 'declined' ? 403 : 410,
						headers: corsHeaders,
						json: errorJson(outcome)
					});
				}
				const body = await sent.promise;
				return route.fulfill({
					headers: {
						...corsHeaders,
						'Content-Type': 'application/octet-stream',
						'X-Flick-Pipe-Salt': salt,
						'Flick-Kind': 'text'
					},
					body
				});
			}
			case '/api/pipe/send': {
				const body = request.postDataBuffer();
				sent.resolve(body);
				return route.fulfill({
					headers: corsHeaders,
					json: { status: 'completed', bytes_transferred: body.length }
				});
			}
			case '/api/pipe/cancel':
				return route.fulfill({ headers: corsHeaders, json: { status: 'cancelled' } });
			default:
				return route.continue();
		}
	}

	return { handle, joinCalls };
}

async function readOwnNumber(page) {
	const label = await page.getByRole('img', { name: /^Your number/ }).getAttribute('aria-label');
	return label.replace('Your number ', '').replaceAll(' ', '');
}

test('Waiting room: three receivers, sender denies one and sends to the number read aloud', async () => {
	const relay = createRelayMock();
	const contexts = [];
	const openPage = async () => {
		const context = await browser.newContext();
		contexts.push(context);
		const page = await context.newPage();
		await routeApi(page, (path, route) => relay.handle(page, path, route));
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		return page;
	};

	try {
		const sender = await openPage();
		const message = 'Real-time live relay transfer test';
		await sender.getByLabel('Message', { exact: true }).fill(message);
		await sender
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Send with code' })
			.click();
		await sender.getByRole('switch', { name: 'Require confirmation before transfer' }).click();
		await sender.getByRole('button', { name: 'Start live transfer' }).click();
		await sender.getByLabel('Transfer code 741-928').waitFor();

		// Three devices enter the same code; each shows its own number.
		const receivers = [];
		for (let i = 0; i < 3; i++) {
			const receiver = await openPage();
			await openReceiveDialog(receiver);
			await receiver.locator('#pipe-code-input').fill('741928');
			await receiver.getByRole('heading', { name: 'Read this to the sender' }).waitFor();
			receivers.push(receiver);
		}
		const [impostor, intended, bystander] = receivers;
		const intendedNumber = await readOwnNumber(intended);
		assert.notEqual(await readOwnNumber(impostor), intendedNumber);

		await sender.getByText('3 of 10 devices waiting').waitFor();

		// Deny one row: only that device ends, the code stays live.
		await sender.getByRole('button', { name: 'Deny connection 1' }).click();
		const impostorDialog = impostor.getByRole('dialog', { name: 'Receive secret with code' });
		await impostorDialog.getByRole('heading', { name: 'The sender declined this device' }).waitFor();
		await sender.getByText('2 of 10 devices waiting').waitFor();

		// The declined browser remembers the decline: re-entering the code asks nothing.
		const joinsBefore = relay.joinCalls.get(impostor);
		await impostorDialog.getByRole('button', { name: 'Enter a new code' }).click();
		await impostor.locator('#pipe-code-input').fill('741928');
		await impostorDialog.getByRole('heading', { name: 'The sender declined this device' }).waitFor();
		assert.equal(relay.joinCalls.get(impostor), joinsBefore);

		// Send to the row whose number the intended receiver reads out.
		await sender
			.getByRole('button', { name: `Send to connection 2, number ${intendedNumber}` })
			.click();

		await intended.getByText('Secret decrypted directly in memory').waitFor();
		assert.equal(
			await intended
				.getByRole('dialog', { name: 'Receive secret with code' })
				.locator('textarea')
				.inputValue(),
			message
		);
		await bystander
			.getByRole('dialog', { name: 'Receive secret with code' })
			.getByRole('heading', { name: 'The sender chose another device' })
			.waitFor();
		await sender.getByRole('heading', { name: 'Transfer complete!' }).waitFor();
		await sender.getByText('live pipe burned').waitFor();
	} finally {
		for (const context of contexts) {
			await context.close();
		}
	}
});
