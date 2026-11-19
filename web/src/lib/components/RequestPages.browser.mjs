// biome-ignore-all lint/style/useNamingConvention: request fixtures use canonical API field names.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: Node test helpers are called only by awaited tests.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';
import { openResponse, requestVectors } from './request-vectors.mjs';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const vectors = await requestVectors();
const submitToken = 'A'.repeat(43);
const receiveToken = `${'B'.repeat(42)}A`;
const submitPath = `/r/${vectors.request_id}#submit=${submitToken}&fp=${vectors.fingerprint}`;
const ownerPath = `/r/${vectors.request_id}/receive#receive=${receiveToken}&key=${vectors.identity}`;
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
	let encryptedNote;
	let fingerprint = vectors.fingerprint;
	let state = options.state ?? 'waiting';
	let generation = 1;
	let payload = options.payload ?? vectors.text.payload;
	let lostSubmit = options.lostSubmit;
	let statusError = options.statusError;
	const expires = options.expires ?? '2099-01-01T00:00:00Z';
	const metadata = () => ({ public_key: publicKey, fingerprint, expires_at: expires });
	function handleSubmission(route) {
		bodies.push(route.request().postData());
		const {
			generation: submittedGen,
			attempt_token,
			...encrypted
		} = route.request().postDataJSON();
		assert.equal(submittedGen, generation);
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
		return route.fulfill({ json: { generation, state: 'submitted' } });
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
		await context.route(
			(url) => url.pathname.startsWith('/api/'),
			(route) => {
				const request = route.request();
				const path = new URL(request.url()).pathname;
				calls.push({ path, headers: request.headers(), body: request.postData() ?? '' });
				const handlers = {
					config: () =>
						route.fulfill({
							json: options.config ?? { payload_inline_max_bytes: 1024, max_file_bytes: 512 }
						}),
					requests: () => {
						const body = request.postDataJSON();
						publicKey = body.public_key;
						encryptedNote = body.encrypted_note;
						fingerprint = createHash('sha256').update(publicKey).digest('base64url');
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
							json: {
								generation,
								state:
									state === 'submitted'
										? 'accepted'
										: state === 'uploading'
											? 'uploading'
											: 'waiting'
							}
						}),
					upload: () => {
						const body = request.postDataJSON();
						state = 'uploading';
						return route.fulfill({
							json: {
								generation,
								state,
								reservation_expires_at: expires,
								upload: {
									url: `${baseUrl}/synthetic-upload`,
									method: 'PUT',
									expires_at: expires,
									headers: { 'Content-Length': String(body.size_bytes) }
								}
							}
						});
					},
					abandon: () => {
						generation += 1;
						state = 'waiting';
						return route.fulfill({ json: { generation, state: 'waiting' } });
					},
					open: () => {
						assert.equal(request.postData(), null);
						state = 'consumed';
						return options.lostOpen ? route.abort('failed') : route.fulfill(openResponse(payload));
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
							json: {
								...metadata(),
								generation,
								can_submit: state === 'waiting',
								...(state === 'waiting' && encryptedNote ? { encrypted_note: encryptedNote } : {})
							}
						});
			}
		);
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
		await owner.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await owner.getByRole('button', { name: 'Create request', exact: true }).click();
		await owner.waitForURL('**/receive#receive=*');
		await readyOwner(owner);
		await owner.getByText('Checking automatically', { exact: true }).waitFor();
		// Copy is the one primary action; Refresh, Share, QR and Cancel stay compact at phone width.
		assert.equal(
			(
				await owner.getByRole('button', { name: 'Copy submission link', exact: true }).textContent()
			).trim(),
			'Copy'
		);
		const card = await owner.getByRole('region', { name: 'Request status' }).boundingBox();
		const compact = {};
		for (const name of ['Refresh status', 'Share link', 'Show submission QR', 'Cancel request']) {
			compact[name] = await owner.getByRole('button', { name, exact: true }).boundingBox();
			assert.ok(compact[name].width < card.width * 0.6, name);
		}
		assert.equal(compact['Share link'].y, compact['Show submission QR'].y);
		const submit = await owner.getByLabel('Submission link', { exact: true }).inputValue();
		const receive = await owner.getByLabel('Your receiving link', { exact: true }).inputValue();
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
		await owner.getByRole('button', { name: 'Copy your receiving link', exact: true }).click();
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
		await owner.getByRole('button', { name: 'Open once', exact: true }).waitFor();
		// A ready submission is no longer something to wait for.
		assert.equal(await owner.getByText('Checking automatically', { exact: true }).count(), 0);
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		assert.equal(
			await owner.getByLabel('Received secret').inputValue(),
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
		assert.equal(await owner.getByLabel('Received secret').count(), 0);
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

test('credential templates submit as FLCR1 text and open in the credential view', async () => {
	// en-US pins the thousands separator in the "1,024 bytes" limit message.
	const senderContext = await browser.newContext({
		viewport: { width: 375, height: 812 },
		locale: 'en-US'
	});
	const ownerContext = await browser.newContext();
	try {
		const mock = fixture();
		await mock.attach(senderContext);
		await mock.attach(ownerContext);
		const sender = await senderContext.newPage();
		await sender.goto(`${baseUrl}${submitPath}`);
		// field-sizing-content ignores rows={7}; the message field keeps the create page's height.
		const message = await sender.getByLabel('Message', { exact: true }).boundingBox();
		assert.ok(message.height >= 176, `message field is ${message.height}px tall`);
		await sender.getByRole('tab', { name: 'Login', exact: true }).click();
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByText('Fill in at least one field.', { exact: true }).waitFor();
		assert.equal(mock.bodies.length, 0);
		// The serialized credential counts against the text limit (1,024 bytes in this fixture).
		await sender.getByLabel('Password', { exact: true }).fill('x'.repeat(1100));
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByText('Choose content between 1 and 1,024 bytes.', { exact: true }).waitFor();
		assert.equal(mock.bodies.length, 0);
		await sender.getByLabel('Username', { exact: true }).fill('synthetic-request-user');
		await sender.getByLabel('Password', { exact: true }).fill('synthetic-request-password');
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
		assert.equal(JSON.parse(mock.bodies[0]).kind, 'text');
		const owner = await ownerContext.newPage();
		await owner.goto(`${baseUrl}${ownerPath}`);
		await readyOwner(owner);
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		await owner.getByText('synthetic-request-user', { exact: true }).waitFor();
		assert.equal(await owner.getByText('synthetic-request-password', { exact: true }).count(), 0);
		await owner.getByRole('button', { name: 'Show Password', exact: true }).click();
		await owner.getByText('synthetic-request-password', { exact: true }).waitFor();
		assert.equal(await owner.getByLabel('Received secret').count(), 0);
		for (const value of ['synthetic-request-user', 'synthetic-request-password']) {
			await privacy(owner, mock.calls, vectors.identity, value);
			await privacy(sender, mock.calls, vectors.identity, value);
			assert.equal(owner.url().includes(value) || sender.url().includes(value), false);
		}
	} finally {
		await senderContext.close();
		await ownerContext.close();
	}
});

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
			// Without a usable key the status reports the submission instead of offering to open it.
			await page.getByText('A submission was accepted.', { exact: true }).waitFor();
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
				await page.getByRole('button', { name: 'Cancel request', exact: true }).click();
				// The in-app ConfirmDialog replaced window.confirm; confirm in the modal.
				await page
					.getByRole('dialog')
					.getByRole('button', { name: 'Cancel request', exact: true })
					.click();
				await page.getByRole('heading', { name: 'Consumed', exact: true }).waitFor();
				const index = mock.calls.findIndex((call) => call.path.endsWith('/revoke'));
				assert.ok(mock.calls[index - 1].path.endsWith('/owner'));
			} else {
				await page.getByRole('button', { name: 'Open once', exact: true }).click();
				await page.getByRole('heading', { name: 'Open outcome unknown', exact: true }).waitFor();
				assert.equal(await page.getByRole('button', { name: 'Open once', exact: true }).count(), 0);
				assert.equal(await page.getByText('You can open it once.', { exact: true }).count(), 0);
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
		await page.getByText('This link does not match the request.', { exact: false }).waitFor();
		assert.equal(await page.getByLabel('Message', { exact: true }).count(), 0);
		await page.goto(`${baseUrl}${submitPath}`);
		await page.getByRole('tab', { name: 'File', exact: true }).click();
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
		// The persisted pageshow makes the page call location.reload(), which tears
		// down the dispatching context mid-call — so the evaluate may reject, and
		// racing it against the Page load event loses the event in CI. Wait for the
		// reloaded page to re-hydrate (the announcer re-attaches after hydration).
		await owner
			.evaluate(() =>
				window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
			)
			.catch(() => undefined);
		await owner.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await readyOwner(owner);
		assert.equal(ownerMock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
		await owner.getByRole('button', { name: 'Open once', exact: true }).click();
		assert.equal(await owner.getByLabel('Received secret').inputValue(), vectors.text.plaintext);
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
		// Same as the owner part above: the pageshow-triggered reload tears down the
		// dispatching context and races the load event, so wait for re-hydration.
		await sender
			.evaluate(() =>
				window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
			)
			.catch(() => undefined);
		await sender.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await sender.getByText('What you were sending was lost', { exact: false }).waitFor();
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
		await owner.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await owner.getByRole('button', { name: 'Create request', exact: true }).click();
		await owner.getByText('Could not verify size limits.', { exact: false }).waitFor();
		assert.equal(await owner.getByRole('button', { name: 'Open once', exact: true }).count(), 0);
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/open')).length, 0);
		await sender.goto(await owner.getByLabel('Submission link', { exact: true }).inputValue());
		await sender.getByRole('tab', { name: 'File', exact: true }).click();
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

test('private requester status card keeps the refresh button clear of status text when text is enlarged', async () => {
	const context = await browser.newContext({ viewport: { width: 360, height: 700 } });
	try {
		const mock = fixture({ state: 'submitted' });
		await mock.attach(context);
		const page = await context.newPage();
		await page.goto(`${baseUrl}${ownerPath}`);
		await readyOwner(page);
		await page.evaluate(() => {
			document.documentElement.style.fontSize = '150%';
		});
		// The status text and button animate their new size (transition-all); measure once it settles.
		await page.waitForFunction(
			() =>
				document.querySelector('[aria-label="Request status"]').getAnimations({ subtree: true })
					.length === 0
		);
		const boxes = await page.evaluate(() => {
			const box = (element) => element.getBoundingClientRect().toJSON();
			const status = document.querySelector('[aria-label="Request status"] p[role="status"]');
			const refresh = [...document.querySelectorAll('button')].find(
				(button) => button.textContent.trim() === 'Refresh status'
			);
			return {
				status: box(status),
				refresh: box(refresh),
				overflow: document.documentElement.scrollWidth > innerWidth
			};
		});
		assert.equal(boxes.overflow, false);
		const overlaps =
			boxes.status.left < boxes.refresh.right &&
			boxes.refresh.left < boxes.status.right &&
			boxes.status.top < boxes.refresh.bottom &&
			boxes.refresh.top < boxes.status.bottom;
		assert.equal(overlaps, false);
	} finally {
		await context.close();
	}
});

test('encrypted request note stays out of API and retrieval links, wraps at 360px/1280px, and fails closed without #mk=', async () => {
	const unbrokenNote = `Memo:${'Z'.repeat(200)}`;
	for (const width of [360, 1280]) {
		const ownerContext = await browser.newContext({ viewport: { width, height: 800 } });
		const senderContext = await browser.newContext({ viewport: { width, height: 800 } });
		ownerContext.setDefaultTimeout(5000);
		senderContext.setDefaultTimeout(5000);
		try {
			const mock = fixture({
				config: { payload_inline_max_bytes: 32, max_file_bytes: 512 }
			});
			await mock.attach(ownerContext);
			await mock.attach(senderContext);
			await senderContext.route('**/synthetic-upload', (route) => route.abort('failed'));
			const owner = await ownerContext.newPage();
			const sender = await senderContext.newPage();

			await owner.goto(`${baseUrl}/request`);
			await owner.locator('#svelte-announcer').waitFor({ state: 'attached' });

			// Multi-byte UTF-8 notes above 1024 bytes show an alert and disable creation before any POST.
			await owner.getByLabel('Note', { exact: true }).fill('가'.repeat(342));
			await owner.getByText('Note must be at most', { exact: false }).waitFor();
			assert.equal(
				await owner.getByRole('button', { name: 'Create request', exact: true }).isDisabled(),
				true
			);
			assert.equal(mock.calls.filter((call) => call.path === '/api/requests').length, 0);

			await owner.getByLabel('Note', { exact: true }).fill(unbrokenNote);
			assert.equal(
				await owner.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);
			await owner.getByRole('button', { name: 'Create request', exact: true }).click();
			await owner.waitForURL('**/receive#receive=*');
			await readyOwner(owner);

			const submitLink = await owner.getByLabel('Submission link', { exact: true }).inputValue();
			const receiveLink = await owner
				.getByLabel('Your receiving link', { exact: true })
				.inputValue();
			const hashParams = new URLSearchParams(new URL(submitLink).hash.slice(1));
			const memoKey = hashParams.get('mk');
			assert.ok(memoKey?.startsWith('AGE-SECRET-KEY-PQ-1'));
			assert.equal(receiveLink.includes('mk='), false);
			assert.equal(receiveLink.includes(memoKey), false);
			assert.equal(JSON.stringify(mock.calls).includes(unbrokenNote), false);
			assert.equal(JSON.stringify(mock.calls).includes(memoKey), false);

			await sender.goto(submitLink);
			const noteRegion = sender.getByRole('region', { name: 'Note from requester', exact: true });
			await noteRegion.waitFor({ state: 'visible' });
			assert.equal((await noteRegion.textContent())?.includes(unbrokenNote), true);
			assert.equal(
				await sender.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
				true
			);

			// Failed large upload abandoned into renewed generation 2 preserves and re-verifies note.
			if (width === 360) {
				await sender.getByRole('tab', { name: 'File', exact: true }).click();
				await sender.locator('#request-file').setInputFiles({
					name: 'large-note-renew.bin',
					mimeType: 'application/octet-stream',
					buffer: Buffer.alloc(64, 3)
				});
				await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
				await sender.getByText('Submission outcome unknown.', { exact: true }).waitFor();
				await sender.getByRole('button', { name: 'Cancel upload', exact: true }).click();
				await noteRegion.waitFor({ state: 'visible' });
				assert.equal((await noteRegion.textContent())?.includes(unbrokenNote), true);
				await sender.getByRole('tab', { name: 'Note', exact: true }).click();
			}

			// Stripping #mk= rejects before allowing submission.
			const noMemoPage = await senderContext.newPage();
			const stripped = new URL(submitLink);
			stripped.hash = `submit=${hashParams.get('submit')}&fp=${hashParams.get('fp')}`;
			await noMemoPage.goto(stripped.toString());
			await noMemoPage.getByText('Ask the requester for the complete submission link.').waitFor();
			assert.equal(await noMemoPage.getByRole('button', { name: 'Encrypt and submit' }).count(), 0);

			// Wrong/corrupted #mk= fails closed before allowing submission.
			const corruptPage = await senderContext.newPage();
			const corrupted = new URL(submitLink);
			const badKey = `${memoKey.slice(0, -4)}AAAA`;
			corrupted.hash = `submit=${hashParams.get('submit')}&fp=${hashParams.get('fp')}&mk=${badKey}`;
			await corruptPage.goto(corrupted.toString());
			await corruptPage.getByText('Could not open the requester note.', { exact: false }).waitFor();
			assert.equal(
				await corruptPage.getByRole('button', { name: 'Encrypt and submit' }).count(),
				0
			);

			// Submitting a response clears the requester note from the screen.
			await sender.getByLabel('Message', { exact: true }).fill('ok');
			await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.equal(await noteRegion.count(), 0);
		} finally {
			await ownerContext.close();
			await senderContext.close();
		}
	}
});

test('whitespace-only request note omits encrypted_note/#mk= and extra #mk= without server note fails closed', async () => {
	const ownerContext = await browser.newContext();
	const senderContext = await browser.newContext();
	try {
		const mock = fixture();
		await mock.attach(ownerContext);
		await mock.attach(senderContext);
		const owner = await ownerContext.newPage();
		await owner.goto(`${baseUrl}/request`);
		await owner.locator('#svelte-announcer').waitFor({ state: 'attached' });
		await owner.getByLabel('Note', { exact: true }).fill('   \n\t  ');
		await owner.getByRole('button', { name: 'Create request', exact: true }).click();
		await owner.waitForURL('**/receive#receive=*');
		await readyOwner(owner);

		const createCall = mock.calls.find((call) => call.path === '/api/requests');
		assert.equal(JSON.parse(createCall.body).encrypted_note, undefined);
		const submitLink = await owner.getByLabel('Submission link', { exact: true }).inputValue();
		assert.equal(submitLink.includes('mk='), false);

		// Extra #mk= on a link without server encrypted_note fails closed.
		const sender = await senderContext.newPage();
		const extra = new URL(submitLink);
		extra.hash += `&mk=${vectors.identity}`;
		await sender.goto(extra.toString());
		await sender.getByText('Ask the requester for the complete submission link.').waitFor();
		assert.equal(await sender.getByRole('button', { name: 'Encrypt and submit' }).count(), 0);
	} finally {
		await ownerContext.close();
		await senderContext.close();
	}
});
