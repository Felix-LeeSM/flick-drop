// biome-ignore-all lint/style/useNamingConvention: request fixtures use canonical API field names.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: Node test helpers are called only by awaited tests.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const vectors = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/request-crypto-v1.json', import.meta.url))
);
const submitToken = 'A'.repeat(43);
const receiveToken = `${'B'.repeat(42)}A`;
const submitPath = `/r/${vectors.request_id}#submit=${submitToken}&fp=${vectors.fingerprint}`;
const ownerPath = `/r/${vectors.request_id}/receive#receive=${receiveToken}&key=${vectors.private_key}`;
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

function fixture(options = {}) {
	const calls = [];
	const bodies = [];
	let publicKey = vectors.public_key;
	let fingerprint = vectors.fingerprint;
	let state = options.state ?? 'waiting';
	let payload = options.payload ?? vectors.text.payload;
	let lostSubmit = options.lostSubmit;
	let statusError = options.statusError;
	const expires = options.expires ?? '2099-01-01T00:00:00Z';
	const metadata = () => ({ public_key: publicKey, fingerprint, expires_at: expires });
	function handleSubmission(route) {
		bodies.push(route.request().postData());
		const { generation, attempt_token, ...encrypted } = route.request().postDataJSON();
		assert.equal(generation, 1);
		assert.equal(Buffer.from(attempt_token, 'base64url').length, 32);
		if (lostSubmit) {
			if (lostSubmit === 'accepted') {
				state = 'submitted';
				payload = encrypted;
			}
			lostSubmit = undefined;
			return route.abort('failed');
		}
		payload = encrypted;
		state = 'submitted';
		return route.fulfill({ json: { generation: 1, state: 'submitted' } });
	}
	const attach = async (context) => {
		await context.addInitScript(() => {
			window.copies = [];
			window.shares = [];
			window.revoked = [];
			const revoke = URL.revokeObjectURL.bind(URL);
			URL.revokeObjectURL = (url) => {
				window.revoked.push(url);
				revoke(url);
			};
			Object.defineProperty(navigator, 'clipboard', {
				configurable: true,
				value: {
					writeText: (value) => {
						window.copies.push(value);
						return Promise.resolve();
					}
				}
			});
			Object.defineProperty(navigator, 'share', {
				configurable: true,
				value: (value) => {
					window.shares.push(value.url);
					return Promise.resolve();
				}
			});
			Object.defineProperty(navigator, 'canShare', { configurable: true, value: () => true });
		});
		await context.route('**/api/**', (route) => {
			const request = route.request();
			const path = new URL(request.url()).pathname;
			calls.push({ path, headers: request.headers(), body: request.postData() ?? '' });
			const handlers = {
				config: () =>
					route.fulfill({
						json: options.config ?? { payload_inline_max_bytes: 1024, max_file_bytes: 512 }
					}),
				requests: () => {
					publicKey = request.postDataJSON().public_key;
					fingerprint = createHash('sha256')
						.update(Buffer.from(publicKey, 'base64'))
						.digest('base64url');
					return route.fulfill({
						json: {
							id: vectors.request_id,
							expires_at: expires,
							submission_token: submitToken,
							retrieval_token: receiveToken
						}
					});
				},
				owner: () => {
					if (statusError) {
						const status = statusError;
						statusError = undefined;
						return route.fulfill({ status, json: {} });
					}
					return route.fulfill({ json: { ...metadata(), state } });
				},
				submit: () => handleSubmission(route),
				attempt: () =>
					route.fulfill({
						json: { generation: 1, state: state === 'submitted' ? 'accepted' : 'waiting' }
					}),
				open: () => {
					assert.equal(request.postData(), null);
					state = 'consumed';
					return options.lostOpen ? route.abort('failed') : route.fulfill({ json: payload });
				},
				revoke: () => {
					assert.equal(request.postData(), null);
					state = options.cancelRace ? 'consumed' : 'cancelled';
					return route.fulfill({ status: options.cancelRace ? 409 : 200, json: { state } });
				}
			};
			const handle = handlers[path.split('/').at(-1)];
			return handle
				? handle()
				: route.fulfill({
						json: { ...metadata(), generation: 1, can_submit: state === 'waiting' }
					});
		});
	};
	return { attach, calls, bodies };
}

async function readyOwner(page) {
	await page.getByRole('button', { name: 'Refresh status', exact: true }).waitFor();
	await page.waitForFunction(() =>
		[...document.querySelectorAll('button')].some(
			(button) => button.textContent.trim() === 'Refresh status' && !button.disabled
		)
	);
}
async function privacy(page, requests, privateKey, plaintext) {
	const stored = await page.evaluate(() =>
		JSON.stringify({
			history: history.state,
			local: { ...localStorage },
			session: { ...sessionStorage }
		})
	);
	assert.equal(stored.includes(privateKey), false);
	assert.equal(stored.includes(plaintext), false);
	assert.deepEqual(await page.evaluate(() => indexedDB.databases()), []);
	assert.equal(JSON.stringify(requests).includes(privateKey), false);
	assert.equal(JSON.stringify(requests).includes(plaintext), false);
}

test('two contexts create, share, submit and retrieve once with private-link reload custody', async () => {
	const ownerContext = await browser.newContext({ viewport: { width: 375, height: 812 } });
	const senderContext = await browser.newContext();
	try {
		const mock = fixture();
		await mock.attach(ownerContext);
		await mock.attach(senderContext);
		const owner = await ownerContext.newPage();
		const sender = await senderContext.newPage();
		await owner.goto(baseUrl);
		await owner.getByRole('link', { name: 'Request a secret', exact: true }).click();
		await owner.getByRole('button', { name: 'Create request', exact: true }).click();
		await owner.waitForURL('**/receive#receive=*');
		await readyOwner(owner);
		const submit = await owner.getByLabel('Submission link', { exact: true }).inputValue();
		const receive = await owner.getByLabel('Private retrieval link', { exact: true }).inputValue();
		const privateKey = new URLSearchParams(new URL(receive).hash.slice(1)).get('key');
		assert.equal(submit.includes(privateKey), false);
		assert.equal(submit.includes(receiveToken), false);
		await owner.getByRole('button', { name: 'Copy submission link', exact: true }).click();
		await owner.getByRole('button', { name: 'Share link', exact: true }).click();
		await owner.getByRole('button', { name: 'Show submission QR', exact: true }).click();
		assert.equal(
			await owner.getByRole('dialog').getByLabel('Share URL', { exact: true }).inputValue(),
			submit
		);
		await owner.keyboard.press('Escape');
		await owner.getByRole('button', { name: 'Copy private retrieval link', exact: true }).click();
		assert.deepEqual(await owner.evaluate(() => window.copies), [submit, receive]);
		assert.deepEqual(await owner.evaluate(() => window.shares), [submit]);
		assert.equal(
			await owner.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
			true
		);
		await owner.reload();
		await readyOwner(owner);
		assert.equal(await owner.getByLabel('Submission link', { exact: true }).count(), 0);
		await sender.goto(submit);
		await sender.getByLabel('Message', { exact: true }).fill('Synthetic private request content');
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
		await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		assert.equal(
			await owner.getByLabel('Decrypted secret').inputValue(),
			'Synthetic private request content'
		);
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 1);
		await privacy(owner, mock.calls, privateKey, 'Synthetic private request content');
		await privacy(sender, mock.calls, privateKey, 'Synthetic private request content');
		for (const call of mock.calls.filter((call) =>
			call.path.includes(`/requests/${vectors.request_id}`)
		)) {
			assert.equal(
				call.headers.authorization,
				`Bearer ${call.path.endsWith('/owner') || call.path.endsWith('/open') ? receiveToken : submitToken}`
			);
		}
		await owner.reload();
		await owner.getByRole('heading', { name: 'Consumed', exact: true }).waitFor();
		assert.equal(await owner.getByLabel('Decrypted secret').count(), 0);
	} finally {
		await ownerContext.close();
		await senderContext.close();
	}
});

for (const outcome of ['waiting', 'accepted']) {
	test(`lost submit response: ${outcome} receipt uses the same attempt, never an automatic retry`, async () => {
		const context = await browser.newContext();
		try {
			const mock = fixture({ lostSubmit: outcome });
			await mock.attach(context);
			const page = await context.newPage();
			await page.goto(`${baseUrl}${submitPath}`);
			await page.getByLabel('Message', { exact: true }).fill('Synthetic retry content');
			await page.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
			await page.getByText('Submission outcome unknown.', { exact: true }).waitFor();
			assert.equal(mock.bodies.length, 1);
			await page.getByRole('button', { name: 'Check submission', exact: true }).click();
			if (outcome === 'waiting') {
				await page.getByRole('button', { name: 'Retry same submission', exact: true }).click();
			}
			await page.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.equal(mock.bodies.length, outcome === 'waiting' ? 2 : 1);
			assert.ok(mock.bodies.every((body) => body === mock.bodies[0]));
		} finally {
			await context.close();
		}
	});
}

for (const fragment of [`#receive=${receiveToken}`, `#receive=${receiveToken}&key=invalid`]) {
	test(`missing or invalid private key cannot consume: ${fragment.endsWith('invalid') ? 'invalid' : 'missing'}`, async () => {
		const context = await browser.newContext();
		try {
			const mock = fixture({ state: 'submitted' });
			await mock.attach(context);
			const page = await context.newPage();
			await page.goto(`${baseUrl}/r/${vectors.request_id}/receive${fragment}`);
			await readyOwner(page);
			assert.equal(
				await page.getByRole('button', { name: 'Open once', exact: true }).isDisabled(),
				true
			);
			assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
			await page.getByRole('alert').waitFor();
		} finally {
			await context.close();
		}
	});
}

test('lost open is never replayed; server conflict wins a cancellation race', async () => {
	for (const race of [false, true]) {
		const context = await browser.newContext();
		try {
			const mock = fixture({ state: 'submitted', lostOpen: !race, cancelRace: race });
			await mock.attach(context);
			const page = await context.newPage();
			await page.goto(`${baseUrl}${ownerPath}`);
			await readyOwner(page);
			if (race) {
				page.on('dialog', (dialog) => dialog.accept());
				await page.getByRole('button', { name: 'Cancel request', exact: true }).click();
				await page.getByRole('heading', { name: 'Consumed', exact: true }).waitFor();
				const index = mock.calls.findIndex((call) => call.path.endsWith('/revoke'));
				assert.ok(mock.calls[index - 1].path.endsWith('/owner'));
			} else {
				await page.getByRole('button', { name: 'Open once', exact: true }).click();
				await page.getByRole('heading', { name: 'Open outcome unknown', exact: true }).waitFor();
				assert.equal(await page.getByRole('button', { name: 'Open once', exact: true }).count(), 0);
				await page.getByRole('button', { name: 'Refresh status', exact: true }).click();
				assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 1);
			}
		} finally {
			await context.close();
		}
	}
});

test('encrypted filenames decrypt locally and expiry clears plaintext and revokes download URLs', async () => {
	const context = await browser.newContext();
	try {
		const mock = fixture({
			state: 'submitted',
			payload: vectors.file.payload,
			expires: new Date(Date.now() + 6000).toISOString()
		});
		await mock.attach(context);
		const page = await context.newPage();
		await page.goto(`${baseUrl}${ownerPath}`);
		await page.getByRole('button', { name: 'Open once', exact: true }).click();
		const download = page.getByRole('link', {
			name: `Download ${vectors.file.filename}`,
			exact: true
		});
		await download.waitFor();
		const blob = await download.getAttribute('href');
		const downloaded = page.waitForEvent('download');
		await download.click();
		assert.deepEqual(
			await readFile(await (await downloaded).path()),
			Buffer.from(vectors.file.plaintext)
		);
		await page.getByRole('heading', { name: 'Request expired', exact: true }).waitFor();
		assert.equal(await download.count(), 0);
		assert.deepEqual(await page.evaluate(() => window.revoked), [blob]);
	} finally {
		await context.close();
	}
});

test('pin mismatch, size ceiling, and 429 status remain actionable without consuming', async () => {
	const context = await browser.newContext();
	try {
		const mock = fixture({ statusError: 429 });
		await mock.attach(context);
		const page = await context.newPage();
		await page.goto(`${baseUrl}${submitPath.replace(vectors.fingerprint, submitToken)}`);
		await page.getByText('The request key does not match this link.', { exact: false }).waitFor();
		assert.equal(await page.getByLabel('Message', { exact: true }).count(), 0);
		await page.goto(`${baseUrl}${submitPath}`);
		await page.getByRole('radio', { name: 'File', exact: true }).check();
		await page.locator('input[type="file"]').setInputFiles({
			name: 'synthetic.bin',
			mimeType: 'application/octet-stream',
			buffer: Buffer.alloc(513)
		});
		await page.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await page.getByText('Choose content between 1 and 512 bytes.', { exact: true }).waitFor();
		assert.equal(mock.bodies.length, 0);
		await page.goto(`${baseUrl}${ownerPath}`);
		await page.getByText('Too many requests.', { exact: false }).waitFor();
		await page.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await readyOwner(page);
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
	} finally {
		await context.close();
	}
});

test('persisted page restore reloads cleared owner/submitter state without replaying operations', async () => {
	const context = await browser.newContext();
	try {
		const ownerMock = fixture({ state: 'submitted' });
		await ownerMock.attach(context);
		const owner = await context.newPage();
		await owner.goto(`${baseUrl}${ownerPath}`);
		await readyOwner(owner);
		await owner.evaluate(() =>
			window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }))
		);
		await Promise.all([
			owner.waitForEvent('load'),
			owner.evaluate(() =>
				window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
			)
		]);
		await readyOwner(owner);
		assert.equal(ownerMock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		assert.equal(await owner.getByLabel('Decrypted secret').inputValue(), vectors.text.plaintext);
	} finally {
		await context.close();
	}
	const senderContext = await browser.newContext();
	try {
		const mock = fixture({ lostSubmit: 'waiting' });
		await mock.attach(senderContext);
		const sender = await senderContext.newPage();
		await sender.goto(`${baseUrl}${submitPath}`);
		await sender.getByLabel('Message', { exact: true }).fill('Synthetic discarded attempt');
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByText('Submission outcome unknown.', { exact: true }).waitFor();
		await sender.evaluate(() =>
			window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }))
		);
		await Promise.all([
			sender.waitForEvent('load'),
			sender.evaluate(() =>
				window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
			)
		]);
		await sender
			.getByText('The previous in-memory attempt was discarded', { exact: false })
			.waitFor();
		await sender.getByLabel('Message', { exact: true }).waitFor();
		assert.equal(mock.bodies.length, 1);
		await sender.getByLabel('Message', { exact: true }).fill('Synthetic deliberate new submission');
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
		assert.equal(mock.bodies.length, 2);
	} finally {
		await senderContext.close();
	}
});

test('failed runtime config cannot consume a custom 2 MiB inline request before verified limits load', async () => {
	const ownerContext = await browser.newContext();
	const senderContext = await browser.newContext();
	try {
		const mock = fixture({
			config: { payload_inline_max_bytes: 4_194_288, max_file_bytes: 4_194_288 }
		});
		await mock.attach(ownerContext);
		await mock.attach(senderContext);
		let failed = false;
		await ownerContext.route('**/api/config', (route) => {
			if (!failed) {
				failed = true;
				return route.fulfill({ status: 503, json: {} });
			}
			return route.fallback();
		});
		const owner = await ownerContext.newPage();
		const sender = await senderContext.newPage();
		await owner.goto(`${baseUrl}/request`);
		await owner.getByRole('button', { name: 'Create request', exact: true }).click();
		await owner.getByText('Could not verify size limits.', { exact: false }).waitFor();
		assert.equal(await owner.getByRole('button', { name: 'Open once', exact: true }).count(), 0);
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
		await sender.goto(await owner.getByLabel('Submission link', { exact: true }).inputValue());
		await sender.getByRole('radio', { name: 'File', exact: true }).check();
		await sender.locator('input[type="file"]').setInputFiles({
			name: 'synthetic-2m.bin',
			mimeType: 'application/octet-stream',
			buffer: Buffer.alloc(2_097_152, 7)
		});
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
		await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		await owner.getByRole('link', { name: 'Download synthetic-2m.bin', exact: true }).waitFor();
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 1);
	} finally {
		await ownerContext.close();
		await senderContext.close();
	}
});
