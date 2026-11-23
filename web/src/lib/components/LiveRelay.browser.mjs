// biome-ignore-all lint/style/useNamingConvention: API fixtures use canonical wire-format names.
// biome-ignore-all lint/suspicious/useAwait: Playwright route handler interface accepts async callback.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';
import { assertDialogFits } from './dialog-fit.mjs';

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
	'Access-Control-Expose-Headers': 'Flick-Kind, Flick-Encrypted-Filename'
};

async function openReceiveDialog(page) {
	await page
		.getByRole('group', { name: 'Delivery method' })
		.getByRole('button', { name: 'Live transfer' })
		.click();
	await page.getByRole('button', { name: 'Enter it here' }).click();
}

// Answers /api/config and CORS preflights; hands every other /api/ call to handle(path, route).
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

test('The header Code button opens the Receive dialog from the default link tab', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (_path, route) => route.continue());
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await page.locator('header').getByRole('button', { name: 'Enter Code' }).click();

		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await dialog.locator('#pipe-code-input').waitFor({ state: 'visible' });
	} finally {
		await page.close();
	}
});

test('The code tab Enter-it-here button opens the Receive dialog', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (_path, route) => route.continue());

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		await openReceiveDialog(page);
		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		await assert.doesNotReject(dialog.waitFor({ state: 'visible' }));

		const codeInput = page.locator('#pipe-code-input');
		await assert.doesNotReject(codeInput.waitFor({ state: 'visible' }));

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
						verify_code: '042',
						device: 'Chrome on Linux',
						expires_in: 170
					}
				});
			}
			// Never answered, so the dialog stays on step 2 waiting for the sender's pick.
			if (path === '/api/pipe/status' || path === '/api/pipe/receive') {
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
		// Forced colors drop the ring; a transparent outline on the focused cap repaints visible.
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

		// A code transfer is encrypted to this device's own key, so no passphrase is asked.
		assert.equal(await dialog.getByLabel('Passphrase', { exact: true }).count(), 0);

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

// Playwright는 가상 키보드를 흉내 내지 못해, 보이는 영역을 숫자 키패드 높이 추정값만큼 줄여 Connect가 그 안에 남는지로 잰다.
// 글자 150%에서는 키패드가 Connect를 덮으므로(6자리 입력 시 자동 연결) 화면 안에 들어오는지만 잰다.
const phoneKeypadHeight = 290;
for (const viewport of [
	{ width: 320, height: 568 },
	{ width: 360, height: 740 }
]) {
	test(`Receive dialog centers on screen, keeps Connect above the keypad at 100% text, and fits ${viewport.width}x${viewport.height} at 150% text`, async () => {
		const page = await browser.newPage({ viewport });
		try {
			await routeApi(page, (path, route) => {
				if (path === '/api/pipe/join') {
					return route.fulfill({
						status: 201,
						headers: corsHeaders,
						json: {
							connection_id: 'conn-1',
							verify_code: '042',
							device: 'Chrome on Linux',
							expires_in: 170
						}
					});
				}
				// 응답하지 않아 대화상자가 번호 확인 단계에 머문다.
				if (path === '/api/pipe/status' || path === '/api/pipe/receive') {
					return;
				}
				return route.continue();
			});
			await page.goto(`${baseUrl}/`);
			await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
			await openReceiveDialog(page);
			const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
			await dialog.getByRole('heading', { name: 'Connect to sender' }).waitFor();
			await assertDialogFits(page, '100% code entry');
			const box = await dialog.boundingBox();
			assert.ok(
				Math.abs(box.y + box.height / 2 - viewport.height / 2) <= 1,
				`dialog spans ${box.y}-${box.y + box.height}px, not centered in ${viewport.height}px`
			);

			const visibleHeight = viewport.height - phoneKeypadHeight;
			await page.setViewportSize({ width: viewport.width, height: visibleHeight });
			await page.waitForFunction(
				(height) =>
					document.querySelector('[role="dialog"]').getBoundingClientRect().bottom <= height,
				visibleHeight
			);
			const connect = await dialog
				.getByRole('button', { name: 'Connect', exact: true })
				.boundingBox();
			assert.ok(
				connect.y >= 0 && connect.y + connect.height <= visibleHeight,
				`Connect spans ${connect.y}-${connect.y + connect.height}px, outside the ${visibleHeight}px above the keypad`
			);
			await page.setViewportSize(viewport);

			await page.evaluate(() => {
				document.documentElement.style.fontSize = '150%';
			});
			await assertDialogFits(page, '150% code entry');
			await page.locator('#pipe-code-input').fill('741928');
			await dialog.getByRole('heading', { name: 'Read this to the sender' }).waitFor();
			await assertDialogFits(page, '150% number check');
		} finally {
			await page.close();
		}
	});
}

test('CreateSecretPage: tab CTAs gate on payload and passphrase switch ships OFF', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (_path, route) => route.continue());

		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });

		const tabs = page.getByRole('group', { name: 'Delivery method' });
		const createSubmit = page.locator('form button[type="submit"]');

		assert.equal(await createSubmit.isDisabled(), true);

		const passphraseSwitch = page.getByRole('switch', {
			name: 'Protect with a passphrase'
		});
		assert.equal(await passphraseSwitch.getAttribute('data-state'), 'unchecked');

		await tabs.getByRole('button', { name: 'Live transfer' }).click();
		const startPipe = page.getByRole('button', { name: 'Start live transfer' });
		assert.equal(
			await page.getByRole('switch', { name: 'Require confirmation before transfer' }).count(),
			0
		);
		assert.equal(await startPipe.isDisabled(), true);

		await page.getByLabel('Message', { exact: true }).fill('Confidential live stream payload');
		assert.equal(await startPipe.isDisabled(), false);

		await tabs.getByRole('button', { name: 'Create link' }).click();
		assert.equal(await createSubmit.isDisabled(), false);
		await passphraseSwitch.click();
		assert.equal(await passphraseSwitch.getAttribute('data-state'), 'checked');
		assert.equal(await createSubmit.isDisabled(), true);
		await page.getByLabel('Passphrase', { exact: true }).fill('extra');
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

		const errorTitle = page.getByRole('heading', { name: 'Transfer Failed' });
		await assert.doesNotReject(errorTitle.waitFor({ state: 'visible' }));
		await page.getByText('Invalid or expired 6-digit code.').waitFor();

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
						expires_in: 180,
						sender_token: 'tok'
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
							recipient: `age1pq1placeholder${i + 1}`,
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
			.getByRole('button', { name: 'Live transfer' })
			.click();
		await page.getByRole('button', { name: 'Start live transfer' }).click();

		await page.getByLabel('Transfer code 741-928').waitFor();
		const list = page.getByRole('list', { name: 'Waiting devices' });
		const count = page.getByText('5 of 10 devices waiting');
		await count.waitFor();
		assert.equal(await count.getAttribute('aria-live'), 'polite');
		await list.getByRole('listitem').first().waitFor();
		assert.equal(await list.getByRole('listitem').count(), 3);

		const more = page.getByRole('button', { name: 'Show 2 more' });
		assert.equal(await more.getAttribute('aria-expanded'), 'false');
		assert.equal(await more.getAttribute('aria-controls'), await list.getAttribute('id'));
		await more.click();
		assert.equal(await list.getByRole('listitem').count(), 5);
		await list.getByText('Samsung Internet on Android').waitFor();
		const fewer = page.getByRole('button', { name: 'Show fewer' });
		assert.equal(await fewer.getAttribute('aria-expanded'), 'true');
		await fewer.click();
		assert.equal(await list.getByRole('listitem').count(), 3);

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

// In-memory /api/pipe waiting room shared by every page in the test. Records any request
// carrying an age identity, which must never leave a browser.
function createRelayMock() {
	const leakedIdentities = [];
	const joinBodies = [];
	const numbers = ['150', '271', '319', '428', '563'];
	const devices = ['Chrome on Windows', 'Safari on iPhone', 'Firefox on Android'];
	const connections = new Map();
	let version = 1;
	let changed = Promise.withResolvers();
	const sent = Promise.withResolvers();
	let cancelCalls = 0;
	const joinCalls = new Map();
	const hold = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

	function bump() {
		version += 1;
		changed.resolve();
		changed = Promise.withResolvers();
	}

	// Settles one connection: 'selected', 'declined', or 'not_selected'.
	function settle(connection, outcome) {
		connection.outcome = outcome;
		connection.decided.resolve(outcome);
	}

	function join(page, route) {
		joinCalls.set(page, (joinCalls.get(page) ?? 0) + 1);
		const body = route.request().postDataJSON();
		joinBodies.push(body);
		const index = connections.size + 1;
		const id = `conn-${index}`;
		const connection = {
			index,
			recipient: body.recipient,
			number: numbers[index - 1],
			device: devices[(index - 1) % devices.length],
			decided: Promise.withResolvers(),
			outcome: null
		};
		connections.set(id, connection);
		bump();
		return route.fulfill({
			status: 201,
			headers: corsHeaders,
			json: {
				connection_id: id,
				verify_code: connection.number,
				device: connection.device,
				expires_in: 170
			}
		});
	}

	async function wait(route) {
		const since = Number(new URL(route.request().url()).searchParams.get('since'));
		if (since === version) {
			await Promise.race([changed.promise, hold(1500)]);
		}
		const pending = [...connections.entries()].filter(([, c]) => c.outcome === null);
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
					recipient: c.recipient,
					connected_seconds: 1
				}))
			}
		});
	}

	async function status(route, connection) {
		const outcome = await Promise.race([
			connection.decided.promise,
			hold(1500).then(() => 'waiting')
		]);
		if (outcome === 'waiting' || outcome === 'selected') {
			return route.fulfill({ headers: corsHeaders, json: { status: outcome } });
		}
		return route.fulfill({
			status: outcome === 'declined' ? 403 : 410,
			headers: corsHeaders,
			json: errorJson(outcome)
		});
	}

	async function receive(route, connection) {
		if (connection.outcome !== 'selected') {
			return route.fulfill({
				status: 409,
				headers: corsHeaders,
				json: errorJson('awaiting_decision')
			});
		}
		const payload = await sent.promise;
		return route.fulfill({
			headers: {
				...corsHeaders,
				'Content-Type': 'application/octet-stream',
				'Flick-Kind': payload.kind,
				...(payload.encryptedFilename
					? { 'Flick-Encrypted-Filename': payload.encryptedFilename }
					: {})
			},
			body: payload.body
		});
	}

	function decide(route, path, chosen) {
		for (const [id, connection] of connections) {
			if (connection.outcome !== null) {
				continue;
			}
			if (id === chosen) {
				settle(connection, path === '/api/pipe/select' ? 'selected' : 'declined');
			} else if (path === '/api/pipe/select') {
				settle(connection, 'not_selected');
			}
		}
		bump();
		return route.fulfill({ headers: corsHeaders, json: { status: 'ok' } });
	}

	function send(route) {
		const headers = route.request().headers();
		const body = route.request().postDataBuffer();
		sent.resolve({
			body,
			kind: headers['flick-kind'] ?? 'text',
			encryptedFilename: headers['flick-encrypted-filename']
		});
		return route.fulfill({
			headers: corsHeaders,
			json: { status: 'completed', bytes_transferred: body.length }
		});
	}

	function handle(page, path, route) {
		const request = route.request();
		const wire = `${JSON.stringify(request.headers())}${request.postData() ?? ''}`;
		if (wire.includes('AGE-SECRET-KEY')) {
			leakedIdentities.push(path);
		}
		const connection = connections.get(route.request().headers()['x-flick-pipe-connection']);
		switch (path) {
			case '/api/pipe/init':
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						code: '741928',
						expires_in: 180,
						sender_token: 'tok'
					}
				});
			case '/api/pipe/join':
				return join(page, route);
			case '/api/pipe/wait':
				return wait(route);
			case '/api/pipe/status':
				return status(route, connection);
			case '/api/pipe/receive':
				return receive(route, connection);
			case '/api/pipe/select':
			case '/api/pipe/decline':
				return decide(route, path, route.request().headers()['x-flick-pipe-connection']);
			case '/api/pipe/send':
				return send(route);
			case '/api/pipe/cancel':
				cancelCalls += 1;
				return route.fulfill({ headers: corsHeaders, json: { status: 'cancelled' } });
			default:
				return route.continue();
		}
	}

	return {
		handle,
		joinCalls,
		joinBodies,
		leakedIdentities,
		getCancelCalls: () => cancelCalls
	};
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
		// A link-tab passphrase must not reach the code transfer: the receiver enters none and decrypts.
		await sender.getByRole('switch', { name: 'Protect with a passphrase' }).click();
		const deliveryMethod = sender.getByRole('group', { name: 'Delivery method' });
		await deliveryMethod.getByRole('button', { name: 'Live transfer' }).click();
		assert.equal(
			await sender.getByRole('button', { name: 'Start live transfer' }).isEnabled(),
			true
		);
		await deliveryMethod.getByRole('button', { name: 'Create link' }).click();
		await sender.getByLabel('Passphrase', { exact: true }).fill('link-tab-only');
		await sender
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Live transfer' })
			.click();
		await sender.getByRole('button', { name: 'Start live transfer' }).click();
		await sender.getByLabel('Transfer code 741-928').waitFor();

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

		await sender.getByRole('button', { name: 'Deny connection 1' }).click();
		const impostorDialog = impostor.getByRole('dialog');
		await impostorDialog
			.getByRole('heading', { name: 'The sender declined this connection' })
			.waitFor();
		assert.equal(await impostorDialog.getByRole('button', { name: 'Enter a new code' }).count(), 0);
		await sender.getByText('2 of 10 devices waiting').waitFor();

		const joinsBefore = relay.joinCalls.get(impostor);
		await impostorDialog.locator('button:has-text("Close")').click();
		await impostorDialog.waitFor({ state: 'hidden' });
		await openReceiveDialog(impostor);
		await impostor.locator('#pipe-code-input').fill('741928');
		await impostorDialog
			.getByRole('heading', { name: 'The sender declined this connection' })
			.waitFor();
		assert.equal(relay.joinCalls.get(impostor), joinsBefore);

		await sender
			.getByRole('button', { name: `Send to connection 2, number ${intendedNumber}` })
			.click();

		await intended.getByRole('heading', { name: 'Secret', exact: true }).waitFor();
		assert.equal(await intended.getByRole('dialog').count(), 0);
		await intended.getByText('Live transfer · Code 741-928').waitFor();
		assert.equal((await intended.getByLabel('Received content').textContent())?.trim(), message);
		await intended.getByRole('button', { name: 'Done', exact: true }).click();
		await intended.getByLabel('Message', { exact: true }).waitFor();

		await bystander
			.getByRole('dialog', { name: 'Receive secret with code' })
			.getByRole('heading', { name: 'The sender chose another device' })
			.waitFor();
		await sender.getByRole('heading', { name: 'Transfer complete', exact: true }).waitFor();
		await sender.getByText('transfer complete', { exact: true }).waitFor();
		// No request from any page may carry an age identity.
		assert.equal(relay.joinBodies.length, 3);
		for (const body of relay.joinBodies) {
			assert.deepEqual(Object.keys(body), ['recipient']);
			assert.match(body.recipient, /^age1pq1[02-9ac-hj-np-z]+$/);
		}
		assert.equal(new Set(relay.joinBodies.map((body) => body.recipient)).size, 3);
		assert.deepEqual(relay.leakedIdentities, []);
	} finally {
		for (const context of contexts) {
			await context.close();
		}
	}
});

// Sender-only waiting room: the caller answers /wait, /select, and /decline.
function senderRoomJson(version, count) {
	return {
		status: 'waiting',
		version,
		connections: Array.from({ length: count }, (_, i) => ({
			connection_id: `c${i + 1}`,
			index: i + 1,
			verify_code: String(101 + i * 111).padStart(3, '0'),
			device: 'Chrome on Windows',
			recipient: `age1pq1placeholder${i + 1}`,
			connected_seconds: 3
		}))
	};
}

function fulfillConfirmedInit(route) {
	return route.fulfill({
		status: 201,
		headers: corsHeaders,
		json: {
			code: '741928',
			expires_in: 180,
			sender_token: 'tok'
		}
	});
}

async function startConfirmedSender(page) {
	await page.goto(`${baseUrl}/`);
	await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
	await page.getByLabel('Message', { exact: true }).fill('payload');
	await page
		.getByRole('group', { name: 'Delivery method' })
		.getByRole('button', { name: 'Live transfer' })
		.click();
	await page.getByRole('button', { name: 'Start live transfer' }).click();
}

async function until(condition, what) {
	const deadline = Date.now() + 5000;
	while (!condition()) {
		if (Date.now() > deadline) {
			throw new Error(`timed out waiting for ${what}`);
		}
		await new Promise((resolve) => setTimeout(resolve, 20));
	}
}

test('Sender: a failed Send keeps exactly one /wait loop running', async () => {
	const page = await browser.newPage();
	const waits = [];
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/init') {
				return fulfillConfirmedInit(route);
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				waits.push(since);
				if (since === '0') {
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(2, 1) });
				}
				return; // unchanged: hold the long-poll
			}
			if (path === '/api/pipe/select') {
				// The device left between the poll and the click.
				return route.fulfill({ status: 409, headers: corsHeaders, json: errorJson('not_pending') });
			}
			return route.continue();
		});
		await startConfirmedSender(page);
		await until(() => waits.length === 2, 'the held /wait');

		const selected = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/pipe/select');
		await page.getByRole('button', { name: 'Send to connection 1, number 101' }).click();
		assert.equal((await selected).status(), 409);
		await page.waitForTimeout(1000);

		// One loop: the first poll, then the one held poll; the failed select starts no second loop.
		assert.deepEqual(waits, ['0', '2']);
		await page.getByRole('button', { name: 'Send to connection 1, number 101' }).waitFor();
	} finally {
		await page.close();
	}
});

for (const width of [320, 360]) {
	test(`Sender: the waiting card keeps the code and its buttons inside ${width}px at 150% text`, async () => {
		const page = await browser.newPage({ viewport: { width, height: 740 } });
		try {
			await routeApi(page, (path, route) => {
				if (path === '/api/pipe/init') {
					return fulfillConfirmedInit(route);
				}
				if (path === '/api/pipe/wait') {
					return; // 대기열 응답을 붙잡아 빈 대기 화면을 유지한다.
				}
				return route.continue();
			});
			await startConfirmedSender(page);
			const code = page.getByLabel('Transfer code 741-928');
			await code.waitFor();
			await page.evaluate(() => {
				document.documentElement.style.fontSize = '150%';
			});
			const fit = await code.evaluate(async (row) => {
				const card = row.closest('section');
				// 글자 크기 변경에 따른 버튼 전환(transition-all)이 끝난 뒤 잰다.
				await Promise.allSettled(
					card
						.getAnimations({ subtree: true })
						.filter(
							(animation) =>
								animation.effect.getComputedTiming().endTime !== Number.POSITIVE_INFINITY
						)
						.map((animation) => animation.finished)
				);
				const box = card.getBoundingClientRect();
				// 가운데 정렬된 코드 줄은 양쪽으로 넘치므로 왼쪽 끝도 잰다.
				const caps = [...row.querySelectorAll('span')].filter((cap) =>
					/^\d$/.test(cap.textContent.trim())
				);
				return {
					caps: caps.length,
					capsInside: caps.every((cap) => {
						const edge = cap.getBoundingClientRect();
						return edge.left >= box.left && edge.right <= box.right;
					}),
					contentInside: card.scrollWidth <= card.clientWidth,
					spilledLabels: [...card.querySelectorAll('button')]
						.filter((button) => button.clientWidth > 0 && button.scrollWidth > button.clientWidth)
						.map((button) => button.textContent.trim()),
					page: document.documentElement.scrollWidth <= innerWidth
				};
			});
			assert.deepEqual(fit, {
				caps: 6,
				capsInside: true,
				contentInside: true,
				spilledLabels: [],
				page: true
			});
		} finally {
			await page.close();
		}
	});
}

test('Sender: a /wait answer that was in flight during Deny does not bring the row back', async () => {
	const page = await browser.newPage();
	const waits = [];
	const declined = Promise.withResolvers();
	try {
		await routeApi(page, async (path, route) => {
			if (path === '/api/pipe/init') {
				return fulfillConfirmedInit(route);
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				waits.push(since);
				if (since === '0') {
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(2, 3) });
				}
				if (since === '2') {
					// Built before the server applied the decline: it still lists connection 1.
					await declined.promise;
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(3, 3) });
				}
				return;
			}
			if (path === '/api/pipe/decline') {
				declined.resolve();
				return route.fulfill({ headers: corsHeaders, json: { status: 'declined' } });
			}
			return route.continue();
		});
		await startConfirmedSender(page);
		await page.getByText('3 of 10 devices waiting').waitFor();

		await page.getByRole('button', { name: 'Deny connection 1' }).click();
		// The stale answer arrived and the sender already polls again with its version.
		await until(() => waits.includes('3'), 'the poll after the stale answer');
		await page.waitForTimeout(200);

		assert.equal(await page.getByRole('button', { name: 'Deny connection 1' }).count(), 0);
		await page.getByText('2 of 10 devices waiting').waitFor();
	} finally {
		await page.close();
	}
});

test('Sender: a device joining while the QR is open closes it so the new row shows', async () => {
	const page = await browser.newPage();
	const joined = Promise.withResolvers();
	try {
		await routeApi(page, async (path, route) => {
			if (path === '/api/pipe/init') {
				return fulfillConfirmedInit(route);
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				if (since === '0') {
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(1, 0) });
				}
				if (since === '1') {
					await joined.promise;
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(2, 1) });
				}
				return;
			}
			return route.continue();
		});
		await startConfirmedSender(page);
		await page.getByText('0 of 10 devices waiting').waitFor();
		await page.getByRole('button', { name: 'Show QR' }).click();
		const qr = page.getByRole('dialog', { name: 'Scan or copy' });
		await qr.waitFor();

		joined.resolve();

		await qr.waitFor({ state: 'hidden' });
		await page.getByText('1 of 10 devices waiting').waitFor();
		await page.getByRole('button', { name: 'Deny connection 1' }).waitFor();
	} finally {
		await page.close();
	}
});

test('Sender: Deny moves focus to the next row, then to the list heading', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/init') {
				return fulfillConfirmedInit(route);
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				if (since === '0') {
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(2, 2) });
				}
				return;
			}
			if (path === '/api/pipe/decline') {
				return route.fulfill({ headers: corsHeaders, json: { status: 'declined' } });
			}
			return route.continue();
		});
		await startConfirmedSender(page);
		await page.getByText('2 of 10 devices waiting').waitFor();
		const focused = () =>
			page.evaluate(
				() => document.activeElement?.getAttribute('aria-label') ?? document.activeElement?.id
			);

		await page.getByRole('button', { name: 'Deny connection 1' }).click();
		await page.getByText('1 of 10 devices waiting').waitFor();
		assert.equal(await focused(), 'Send to connection 2, number 212');

		await page.getByRole('button', { name: 'Deny connection 2' }).click();
		await page.getByText('0 of 10 devices waiting').waitFor();
		assert.equal(await focused(), 'pipe-pending-heading');
	} finally {
		await page.close();
	}
});

test('Sender: a Deny the server did not apply brings the row back with an error', async () => {
	const page = await browser.newPage();
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/init') {
				return fulfillConfirmedInit(route);
			}
			if (path === '/api/pipe/wait') {
				const since = new URL(route.request().url()).searchParams.get('since');
				if (since === '0') {
					return route.fulfill({ headers: corsHeaders, json: senderRoomJson(2, 2) });
				}
				return;
			}
			if (path === '/api/pipe/decline') {
				return route.fulfill({
					status: 500,
					headers: corsHeaders,
					json: errorJson('internal_error')
				});
			}
			return route.continue();
		});
		await startConfirmedSender(page);
		await page.getByText('2 of 10 devices waiting').waitFor();

		await page.getByRole('button', { name: 'Deny connection 1' }).click();

		// The device is still waiting on the server, so its row returns in join order.
		await page.getByText('Could not deny that device. Try again.').waitFor();
		await page.getByText('2 of 10 devices waiting').waitFor();
		const rows = page.getByRole('list', { name: 'Waiting devices' }).getByRole('listitem');
		assert.match(await rows.first().textContent(), /Connection 1/);
	} finally {
		await page.close();
	}
});

test('Receiver: a connection dropped while waiting says so and lets the same code in again', async () => {
	const page = await browser.newPage();
	let joins = 0;
	try {
		await routeApi(page, (path, route) => {
			if (path === '/api/pipe/join') {
				joins += 1;
				return route.fulfill({
					status: 201,
					headers: corsHeaders,
					json: {
						connection_id: `conn-${joins}`,
						verify_code: '042',
						device: 'Chrome on Linux',
						expires_in: 170
					}
				});
			}
			if (path === '/api/pipe/status') {
				// The tab slept past 35 seconds: the server dropped the connection.
				return route.fulfill({ status: 404, headers: corsHeaders, json: errorJson('not_found') });
			}
			return route.continue();
		});
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await openReceiveDialog(page);
		const dialog = page.getByRole('dialog', { name: 'Receive secret with code' });
		const dropped = dialog.getByText(
			'This device was disconnected while waiting. Enter the code again.'
		);
		await page.locator('#pipe-code-input').fill('741928');

		await dropped.waitFor();
		assert.equal(await dialog.getByText('Invalid or expired 6-digit code.').count(), 0);

		// A drop is not a decline: the same code joins again.
		await dialog.getByRole('button', { name: 'Try again' }).click();
		await page.locator('#pipe-code-input').fill('741928');
		await dropped.waitFor();
		assert.equal(joins, 2);
	} finally {
		await page.close();
	}
});

async function withMockLiveTransfer(options, verify) {
	const {
		senderViewport = { width: 1280, height: 800 },
		receiverViewport = { width: 360, height: 740 },
		prepareSender,
		installReceiverInit
	} = options;
	const relay = createRelayMock();
	const contexts = [];
	const openPage = async (viewport, init) => {
		const context = await browser.newContext({ viewport });
		contexts.push(context);
		if (init) {
			await context.addInitScript(init);
		}
		const page = await context.newPage();
		await routeApi(page, (path, route) => relay.handle(page, path, route));
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		return page;
	};
	try {
		const sender = await openPage(senderViewport);
		const receiver = await openPage(receiverViewport, installReceiverInit);
		await prepareSender(sender);
		await sender
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Live transfer' })
			.click();
		await sender.getByRole('button', { name: 'Start live transfer' }).click();
		await sender.getByLabel('Transfer code 741-928').waitFor();

		await receiver.locator('header').getByRole('button', { name: 'Enter Code' }).click();
		await receiver.locator('#pipe-code-input').fill('741928');
		await receiver.getByRole('heading', { name: 'Read this to the sender' }).waitFor();
		const number = await readOwnNumber(receiver);
		const sendButton = sender.getByRole('button', {
			name: `Send to connection 1, number ${number}`
		});
		await sendButton.waitFor();
		const senderOverflow = await sender.evaluate(
			() => document.documentElement.scrollWidth > innerWidth
		);

		await sendButton.click();
		await receiver.getByRole('heading', { name: 'Secret', exact: true }).waitFor();
		await verify({ sender, receiver, relay, senderOverflow });
	} finally {
		for (const context of contexts) {
			await context.close();
		}
	}
}

test('Received live secret wraps a 200-character unbroken value cleanly inside 360px', async () => {
	const unbroken = 'W'.repeat(200);
	await withMockLiveTransfer(
		{
			prepareSender: async (sender) => {
				await sender.getByLabel('Message', { exact: true }).fill(unbroken);
			}
		},
		async ({ receiver }) => {
			assert.equal((await receiver.getByLabel('Received content').textContent())?.trim(), unbroken);
			assert.equal(
				await receiver.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
		}
	);
});

test('Received live file wraps a 200-character filename at 320px/360px and downloads exact bytes', async () => {
	const longFileName = `${'F'.repeat(200)}.bin`;
	const expectedBytes = Buffer.from('live-transfer-file-payload-123');
	await withMockLiveTransfer(
		{
			senderViewport: { width: 320, height: 740 },
			receiverViewport: { width: 360, height: 740 },
			prepareSender: async (sender) => {
				await sender.getByRole('tab', { name: 'File', exact: true }).click();
				await sender.locator('#secret-file').setInputFiles({
					name: longFileName,
					mimeType: 'application/octet-stream',
					buffer: expectedBytes
				});
			}
		},
		async ({ receiver, senderOverflow }) => {
			assert.equal(senderOverflow, false, 'sender waiting panel overflowed narrow viewport');
			await receiver.getByText(longFileName, { exact: true }).waitFor();
			assert.equal(
				await receiver.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
			const [download] = await Promise.all([
				receiver.waitForEvent('download'),
				receiver.getByRole('link', { name: `Save ${longFileName}` }).click()
			]);
			assert.equal(download.suggestedFilename(), longFileName);
			const chunks = [];
			for await (const chunk of await download.createReadStream()) {
				chunks.push(chunk);
			}
			assert.deepEqual(Buffer.concat(chunks), expectedBytes);
		}
	);
});

async function routeActiveWaitCancellation(page, fallbackRelay) {
	let localCancels = 0;
	const localCancelled = Promise.withResolvers();
	await page.unrouteAll();
	await routeApi(page, async (path, route) => {
		const token = route.request().headers()['x-flick-pipe-sender'];
		if (path === '/api/pipe/init') {
			return route.fulfill({
				status: 201,
				headers: corsHeaders,
				json: { code: '111222', expires_in: 180, sender_token: 'local-tok' }
			});
		}
		if (path === '/api/pipe/wait' && token === 'local-tok') {
			const since = Number(new URL(route.request().url()).searchParams.get('since') ?? '0');
			if (since >= 1) {
				await new Promise((resolve) => setTimeout(resolve, 1500));
			}
			return route.fulfill({
				headers: corsHeaders,
				json: { status: 'waiting', version: 1, connections: [] }
			});
		}
		if (path === '/api/pipe/cancel' && token === 'local-tok') {
			localCancels += 1;
			localCancelled.resolve();
			return route.fulfill({ headers: corsHeaders, json: { status: 'cancelled' } });
		}
		return fallbackRelay.handle(page, path, route);
	});
	return {
		waitForCancel: () => localCancelled.promise,
		getCancels: () => localCancels
	};
}

test('Received live credential copies formatted text at 360px and receiving cancels an active wait', async () => {
	const unbrokenSecret = 'S'.repeat(200);
	await withMockLiveTransfer(
		{
			prepareSender: async (page) => {
				await page.getByRole('tab', { name: 'Login', exact: true }).click();
				await page.getByLabel('Username', { exact: true }).fill('live-user');
				await page.getByLabel('Password', { exact: true }).fill(unbrokenSecret);
			},
			installReceiverInit: () => {
				window.copies = [];
				Object.defineProperty(navigator, 'clipboard', {
					configurable: true,
					value: {
						writeText: (value) => {
							window.copies.push(value);
							return Promise.resolve();
						}
					}
				});
			}
		},
		async ({ receiver }) => {
			await receiver.getByText('live-user', { exact: true }).waitFor();
			await receiver.getByRole('button', { name: 'Show Password', exact: true }).click();
			await receiver.getByText(unbrokenSecret, { exact: true }).waitFor();
			assert.equal(
				await receiver.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
			await receiver.getByRole('button', { name: 'Copy all', exact: true }).click();
			await receiver.getByRole('button', { name: 'Copied', exact: true }).waitFor();
			const copied = await receiver.evaluate(() => window.copies[0]);
			assert.equal(copied.startsWith('FLCR1:'), false);
			assert.ok(copied.includes('Username: live-user'));
			assert.ok(copied.includes(unbrokenSecret));

			const secondRelay = createRelayMock();
			const senderContext = await browser.newContext();
			try {
				const peerSender = await senderContext.newPage();
				await routeApi(peerSender, (path, route) => secondRelay.handle(peerSender, path, route));
				await peerSender.goto(`${baseUrl}/`);
				await peerSender.locator('#svelte-announcer').waitFor({ state: 'attached' });

				const localWait = await routeActiveWaitCancellation(receiver, secondRelay);

				await receiver.getByRole('button', { name: 'Done', exact: true }).click();
				await receiver.getByLabel('Message', { exact: true }).fill('Waiting secret');
				await receiver
					.getByRole('group', { name: 'Delivery method' })
					.getByRole('button', { name: 'Live transfer' })
					.click();
				await receiver.getByRole('button', { name: 'Start live transfer' }).click();
				await receiver.getByLabel('Transfer code 111-222').waitFor();

				await peerSender.getByLabel('Message', { exact: true }).fill('Peer override secret');
				await peerSender
					.getByRole('group', { name: 'Delivery method' })
					.getByRole('button', { name: 'Live transfer' })
					.click();
				await peerSender.getByRole('button', { name: 'Start live transfer' }).click();
				await peerSender.getByLabel('Transfer code 741-928').waitFor();

				await receiver.locator('header').getByRole('button', { name: 'Enter Code' }).click();
				await receiver.locator('#pipe-code-input').fill('741928');
				await receiver.getByRole('heading', { name: 'Read this to the sender' }).waitFor();
				const secondNumber = await readOwnNumber(receiver);
				await peerSender
					.getByRole('button', { name: `Send to connection 1, number ${secondNumber}` })
					.click();
				await Promise.all([
					receiver.getByRole('heading', { name: 'Secret', exact: true }).waitFor(),
					localWait.waitForCancel()
				]);
				assert.equal(localWait.getCancels(), 1);
				assert.equal(
					(await receiver.getByLabel('Received content').textContent())?.trim(),
					'Peer override secret'
				);
				const dropBlocked = await receiver.evaluate(() => {
					const transfer = new DataTransfer();
					transfer.items.add(
						new File(['ignored'], 'dropped-while-received.txt', { type: 'text/plain' })
					);
					const dragHandled = !document.body.dispatchEvent(
						new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: transfer })
					);
					const dropHandled = !document.body.dispatchEvent(
						new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer })
					);
					return !dragHandled && !dropHandled;
				});
				assert.equal(dropBlocked, true);
				assert.equal(
					(await receiver.getByLabel('Received content').textContent())?.trim(),
					'Peer override secret'
				);
				await receiver.getByRole('button', { name: 'Done', exact: true }).click();
				assert.equal(await receiver.getByText('dropped-while-received.txt').count(), 0);
			} finally {
				await senderContext.close();
			}
		}
	);
});

test('Sender: the logo on the home page cancels a waiting live transfer on the server', async () => {
	const relay = createRelayMock();
	const context = await browser.newContext();
	try {
		const sender = await context.newPage();
		await routeApi(sender, (path, route) => relay.handle(sender, path, route));
		await sender.goto(`${baseUrl}/`);
		await sender.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await sender.getByLabel('Message', { exact: true }).fill('Synthetic logo cancel');
		await sender
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Live transfer' })
			.click();
		await sender.getByRole('button', { name: 'Start live transfer' }).click();
		await sender.getByLabel('Transfer code 741-928').waitFor();

		await sender.getByRole('link', { name: 'Flick', exact: true }).click();

		await sender.getByLabel('Message', { exact: true }).waitFor();
		assert.equal(await sender.getByLabel('Transfer code 741-928').count(), 0);
		// The cancel request is fire-and-forget, so give it a moment to reach the mock.
		for (let i = 0; i < 50 && relay.getCancelCalls() === 0; i++) {
			await sender.waitForTimeout(100);
		}
		assert.equal(relay.getCancelCalls(), 1);
	} finally {
		await context.close();
	}
});
