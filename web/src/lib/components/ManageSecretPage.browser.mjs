// biome-ignore-all lint/style/useNamingConvention: fixtures retain API field names.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const token = 'A'.repeat(43);
const privatePath = `/m/managed#manage=${token}`;
const active = {
	id: 'managed',
	status: 'active',
	expires_at: '2099-01-01T00:00:00Z',
	management_expires_at: '2099-01-01T00:00:00Z',
	can_cancel: true
};
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

// The server forgets an opened delivery and answers every management request
// for it exactly like an unknown ID.
const unknownId = {
	status: 404,
	json: { error: { code: 'management_unavailable', message: 'management link is unavailable' } }
};

async function fixture(page, openRace = false) {
	const requests = [];
	let status = active;
	const terminal = { ...active, status: 'cancelled', can_cancel: false };
	const cancellation = openRace ? unknownId : { json: terminal };
	await page.addInitScript(() => {
		window.shared = [];
		window.copied = [];
		Object.defineProperty(navigator, 'share', {
			configurable: true,
			value: (value) => {
				window.shared.push(value.url);
				return Promise.resolve();
			}
		});
		Object.defineProperty(navigator, 'canShare', { configurable: true, value: () => true });
		Object.defineProperty(navigator, 'clipboard', {
			configurable: true,
			value: {
				writeText: (value) => {
					window.copied.push(value);
					return Promise.resolve();
				}
			}
		});
	});
	await page.route(
		(url) => url.pathname.startsWith('/api/'),
		(route) => {
			const request = route.request();
			const path = new URL(request.url()).pathname;
			requests.push({ path, headers: request.headers(), body: request.postData() ?? '' });
			if (path === '/api/config') {
				return route.fulfill({ json: {} });
			}
			if (path === '/api/secrets') {
				return route.fulfill({
					json: {
						id: active.id,
						expires_at: active.expires_at,
						management_token: token,
						management_expires_at: active.expires_at
					}
				});
			}
			if (path.endsWith('/management')) {
				return route.fulfill(status ? { json: status } : unknownId);
			}
			if (path.endsWith('/revoke')) {
				status = openRace ? null : terminal;
				return route.fulfill(cancellation);
			}
			return route.abort();
		}
	);
	return requests;
}

for (const model of ['a', 'b']) {
	test(`Model ${model}: separate recipient sharing, private management, and refresh custody`, async () => {
		const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
		try {
			const requests = await fixture(page);
			const configured = page.waitForResponse('**/api/config');
			await page.goto(baseUrl);
			await configured;
			await page.getByLabel('Message', { exact: true }).fill('synthetic private message');
			if (model === 'a') {
				await page.getByRole('switch', { name: 'Protect with an additional passphrase' }).click();
				await page
					.getByLabel('Additional passphrase', { exact: true })
					.fill('synthetic private passphrase');
			}
			// The delivery tab shares the CTA's name, so the submit action is picked by its role in the form.
			await page.locator('form button[type="submit"]').click();
			await page.waitForURL(`**${privatePath}`);
			await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
			// Copy is the one primary action; Refresh, Share, QR and Cancel stay compact at phone width.
			assert.equal(
				(
					await page.getByRole('button', { name: 'Copy recipient link', exact: true }).textContent()
				).trim(),
				'Copy'
			);
			const card = await page.getByRole('region', { name: 'Delivery status' }).boundingBox();
			const compact = {};
			for (const name of ['Refresh status', 'Share link', 'Show QR', 'Cancel delivery']) {
				compact[name] = await page.getByRole('button', { name, exact: true }).boundingBox();
				assert.ok(compact[name].width < card.width * 0.6, name);
			}
			assert.equal(compact['Share link'].y, compact['Show QR'].y);
			const recipient = await page.getByLabel('Share URL', { exact: true }).inputValue();
			const management = await page
				.getByLabel('Private management link', { exact: true })
				.inputValue();
			assert.equal(new URL(recipient).pathname, '/s/managed');
			assert.equal(new URL(management).hash, `#manage=${token}`);
			assert.equal(recipient.includes(token), false);
			assert.equal(management.includes('#key='), false);
			assert.equal(
				model === 'b'
					? /^#key=AGE-SECRET-KEY-PQ-1[02-9AC-HJ-NP-Z]+$/.test(new URL(recipient).hash)
					: new URL(recipient).hash === '',
				true
			);
			await page.getByRole('button', { name: 'Copy recipient link', exact: true }).click();
			await page.getByRole('button', { name: 'Share link', exact: true }).click();
			await page.getByRole('button', { name: 'Show QR', exact: true }).click();
			assert.equal(
				await page.getByRole('dialog').getByLabel('Share URL', { exact: true }).inputValue(),
				recipient
			);
			await page.keyboard.press('Escape');
			await page.getByRole('button', { name: 'Copy management link', exact: true }).click();
			assert.deepEqual(await page.evaluate(() => window.shared), [recipient]);
			assert.deepEqual(await page.evaluate(() => window.copied), [recipient, management]);
			const persisted = await page.evaluate(() =>
				JSON.stringify({
					history: history.state,
					local: { ...localStorage },
					session: { ...sessionStorage }
				})
			);
			assert.equal(persisted.includes(recipient), false);
			assert.equal(persisted.includes('synthetic private passphrase'), false);
			if (model === 'b') {
				assert.equal(persisted.includes(new URL(recipient).hash.slice(5)), false);
			}
			assert.deepEqual(await page.evaluate(() => indexedDB.databases()), []);
			assert.equal(
				await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
			assert.equal(
				requests.some((request) => request.path.endsWith('/open')),
				false
			);
			for (const request of requests) {
				assert.equal(request.path.includes(token), false);
				assert.equal(request.body.includes('synthetic private'), false);
				if (model === 'b') {
					assert.equal(JSON.stringify(request).includes(new URL(recipient).hash.slice(5)), false);
				}
				if (request.path.endsWith('/management')) {
					assert.equal(request.headers.authorization, `Bearer ${token}`);
				}
			}
			await page.reload();
			await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
			assert.equal(await page.getByLabel('Share URL', { exact: true }).count(), 0);
			await page
				.getByText('The recipient link is available only in the original creation session.', {
					exact: false
				})
				.waitFor();
			await page.getByRole('button', { name: 'Cancel delivery', exact: true }).click();
			// The in-app ConfirmDialog replaced window.confirm; confirm in the modal.
			await page
				.getByRole('dialog')
				.getByRole('button', { name: 'Cancel delivery', exact: true })
				.click();
			await page.getByRole('heading', { name: 'Cancelled', exact: true }).waitFor();
			// The closing modal keeps its own "Cancel delivery" button until its exit animation ends.
			await page.getByRole('dialog').waitFor({ state: 'detached' });
			assert.equal(
				await page.getByRole('button', { name: 'Cancel delivery', exact: true }).count(),
				0
			);
			const revocation = requests.findIndex((request) => request.path.endsWith('/revoke'));
			assert.ok(revocation > 0 && requests[revocation - 1].path.endsWith('/management'));
		} finally {
			await page.close();
		}
	});
}

test('leaving before the recipient link is handed off asks first, and stops asking after Copy', async () => {
	const page = await browser.newPage();
	try {
		await fixture(page);
		const configured = page.waitForResponse('**/api/config');
		await page.goto(baseUrl);
		await configured;
		await page.getByLabel('Message', { exact: true }).fill('synthetic private message');
		await page.locator('form button[type="submit"]').click();
		await page.waitForURL(`**${privatePath}`);
		await page.getByLabel('Share URL', { exact: true }).waitFor();
		const dialogs = [];
		page.on('dialog', (dialog) => {
			dialogs.push(dialog.type());
			return dialog.dismiss();
		});
		// Given 링크를 아직 넘기지 않은 상태 When 새로고침하면 Then 브라우저가 먼저 묻고, 머무르면 링크가 남는다.
		const asked = page.waitForEvent('dialog');
		await page.evaluate(() => location.reload());
		await asked;
		assert.deepEqual(dialogs, ['beforeunload']);
		assert.equal(await page.getByLabel('Share URL', { exact: true }).count(), 1);
		// Given Copy로 링크를 넘긴 뒤 When 새로고침하면 Then 묻지 않고 떠난다.
		await page.getByRole('button', { name: 'Copy recipient link', exact: true }).click();
		await page.reload();
		await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
		assert.deepEqual(dialogs, ['beforeunload']);
	} finally {
		await page.close();
	}
});

test('an open that wins the cancellation race reads as an unavailable link', async () => {
	const page = await browser.newPage();
	try {
		await fixture(page, true);
		await page.goto(`${baseUrl}${privatePath}`);
		await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
		assert.equal(await page.getByLabel('Share URL', { exact: true }).count(), 0);
		await page.getByRole('button', { name: 'Cancel delivery', exact: true }).click();
		// The in-app ConfirmDialog replaced window.confirm; confirm in the modal.
		await page
			.getByRole('dialog')
			.getByRole('button', { name: 'Cancel delivery', exact: true })
			.click();
		await page.getByText('Management link unavailable.', { exact: true }).waitFor();
		assert.equal(await page.getByRole('heading', { name: 'Cancelled', exact: true }).count(), 0);
		assert.equal(await page.getByLabel('Private management link', { exact: true }).count(), 0);
		assert.equal(await page.title(), 'Manage delivery - Flick');
	} finally {
		await page.close();
	}
});

test('the creation session lands on the unavailable card once the delivery is opened', async () => {
	const page = await browser.newPage();
	try {
		await page.route('**/api/config', (route) => route.fulfill({ json: {} }));
		await page.route('**/api/secrets', (route) =>
			route.fulfill({
				json: {
					id: active.id,
					expires_at: active.expires_at,
					management_token: token,
					management_expires_at: active.expires_at
				}
			})
		);
		let opened = false;
		await page.route('**/api/secrets/managed/management', (route) =>
			route.fulfill(opened ? unknownId : { json: active })
		);
		const configured = page.waitForResponse('**/api/config');
		await page.goto(baseUrl);
		await configured;
		await page.getByLabel('Message', { exact: true }).fill('synthetic private message');
		await page.locator('form button[type="submit"]').click();
		await page.waitForURL(`**${privatePath}`);
		await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
		assert.equal(await page.title(), 'Manage delivery - Flick');
		// The recipient opens it; the next status check meets the unknown-ID response.
		opened = true;
		await page.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await page.getByText('Management link unavailable.', { exact: true }).waitFor();
		assert.equal(await page.getByLabel('Share URL', { exact: true }).count(), 0);
		assert.equal(await page.getByLabel('Private management link', { exact: true }).count(), 0);
		assert.equal(
			await page.getByText('check or cancel this delivery', { exact: false }).count(),
			0
		);
		assert.equal(await page.title(), 'Manage delivery - Flick');
	} finally {
		await page.close();
	}
});

test('invalid links and network failures do not fabricate a delivery outcome', async () => {
	const page = await browser.newPage();
	try {
		const requests = await fixture(page);
		await page.goto(`${baseUrl}/m/managed#manage=invalid`);
		await page.getByText('Management link unavailable.', { exact: true }).waitFor();
		assert.equal(requests.length, 0);
		await page.route('**/api/secrets/managed/management', (route) =>
			route.fulfill({ status: 503, json: {} })
		);
		await page.goto(`${baseUrl}${privatePath}`);
		await page.getByRole('alert').waitFor();
		await page.getByText('Delivery status is unknown.', { exact: true }).waitFor();
		assert.equal(
			await page.getByRole('button', { name: 'Cancel delivery', exact: true }).count(),
			0
		);
		await page.unroute('**/api/secrets/managed/management');
		await page.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
	} finally {
		await page.close();
	}
});

test('known expiry discards link details and shows lockout', async () => {
	const page = await browser.newPage();
	try {
		await page.route('**/api/secrets/managed/management', (route) => {
			const expires = new Date(Date.now() + 1500).toISOString();
			return route.fulfill({
				json: {
					...active,
					status: 'locked',
					can_cancel: false,
					expires_at: expires,
					management_expires_at: expires
				}
			});
		});
		await page.goto(`${baseUrl}${privatePath}`);
		await page.getByRole('heading', { name: 'Locked', exact: true }).waitFor();
		await page.getByText('Management link expired.', { exact: true }).waitFor();
		assert.equal(await page.getByLabel('Private management link', { exact: true }).count(), 0);
		assert.equal(await page.getByRole('heading', { name: 'Locked', exact: true }).count(), 0);
	} finally {
		await page.close();
	}
});

test('status card wraps the refresh button clear of a long title when text is enlarged', async () => {
	const page = await browser.newPage({ viewport: { width: 360, height: 700 } });
	try {
		await page.route('**/api/secrets/managed/management', (route) =>
			route.fulfill({ json: { ...active, status: 'pending_upload', can_cancel: true } })
		);
		await page.goto(`${baseUrl}${privatePath}`);
		await page.getByRole('heading', { name: 'Upload not finished', exact: true }).waitFor();
		await page.evaluate(() => {
			document.documentElement.style.fontSize = '150%';
		});
		// The title and button animate their new size (transition-all); measure once it settles.
		await page.waitForFunction(
			() =>
				document.querySelector('[aria-label="Delivery status"]').getAnimations({ subtree: true })
					.length === 0
		);
		const boxes = await page.evaluate(() => {
			const box = (element) => element.getBoundingClientRect().toJSON();
			const title = document.querySelector('[aria-label="Delivery status"] h2');
			const refresh = [...document.querySelectorAll('button')].find(
				(button) => button.textContent.trim() === 'Refresh status'
			);
			return {
				title: box(title),
				refresh: box(refresh),
				overflow: document.documentElement.scrollWidth > innerWidth
			};
		});
		assert.equal(boxes.overflow, false);
		const overlaps =
			boxes.title.left < boxes.refresh.right &&
			boxes.refresh.left < boxes.title.right &&
			boxes.title.top < boxes.refresh.bottom &&
			boxes.refresh.top < boxes.title.bottom;
		assert.equal(overlaps, false);
	} finally {
		await page.close();
	}
});
