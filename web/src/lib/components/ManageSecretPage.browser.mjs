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

async function fixture(page, conflict = false) {
	const requests = [];
	let status = active;
	const terminal = { ...active, status: conflict ? 'opened' : 'cancelled', can_cancel: false };
	const cancellation = {
		status: conflict ? 409 : 200,
		json: conflict ? { error: { code: 'not_cancellable' }, status: terminal } : terminal
	};
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
	await page.route('**/api/**', (route) => {
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
			return route.fulfill({ json: status });
		}
		if (path.endsWith('/revoke')) {
			status = terminal;
			return route.fulfill(cancellation);
		}
		return route.abort();
	});
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
				await page.getByLabel('Passphrase', { exact: true }).fill('synthetic private passphrase');
			} else {
				await page.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();
			}
			await page.getByRole('button', { name: 'Create link', exact: true }).click();
			await page.waitForURL(`**${privatePath}`);
			await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
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
					? /^#key=[\w-]{43}$/.test(new URL(recipient).hash)
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
			page.on('dialog', (dialog) => dialog.accept());
			await page.getByRole('button', { name: 'Cancel delivery', exact: true }).click();
			await page.getByRole('heading', { name: 'Cancelled', exact: true }).waitFor();
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

test('a fresh management visit reconciles an open/cancel race from the server snapshot', async () => {
	const page = await browser.newPage();
	try {
		await fixture(page, true);
		await page.goto(`${baseUrl}${privatePath}`);
		await page.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
		assert.equal(await page.getByLabel('Share URL', { exact: true }).count(), 0);
		page.on('dialog', (dialog) => dialog.accept());
		await page.getByRole('button', { name: 'Cancel delivery', exact: true }).click();
		await page.getByRole('heading', { name: 'Opened', exact: true }).waitFor();
		await page
			.getByText('This does not confirm that anyone decrypted or read it.', { exact: false })
			.waitFor();
		assert.equal(await page.getByRole('heading', { name: 'Cancelled', exact: true }).count(), 0);
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

test('known expiry discards link details and lockout is distinct from opening', async () => {
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
		assert.equal(await page.getByRole('heading', { name: 'Opened', exact: true }).count(), 0);
		await page.getByText('Management link expired.', { exact: true }).waitFor();
		assert.equal(await page.getByLabel('Private management link', { exact: true }).count(), 0);
		assert.equal(await page.getByRole('heading', { name: 'Locked', exact: true }).count(), 0);
	} finally {
		await page.close();
	}
});
