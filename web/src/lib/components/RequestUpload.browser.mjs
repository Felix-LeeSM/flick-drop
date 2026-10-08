// biome-ignore-all lint/style/useNamingConvention: synthetic fixtures use the request API contract.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: helpers are invoked only from awaited Node tests.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const submitToken = 'A'.repeat(43);
const receiveToken = `${'B'.repeat(42)}A`;
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

async function fixture(context, options = {}) {
	let publicKey = '';
	let fingerprint = '';
	let state = 'waiting';
	let generation = 1;
	let envelope;
	let ciphertext;
	let accepted;
	let lostReserve = options.lostReserve;
	let lostAbandon = options.lostAbandon;
	const calls = [];
	const reservedBodies = [];
	const uploads = [];
	const putStarted = Promise.withResolvers();
	const finalizeStarted = Promise.withResolvers();
	const putGate = Promise.withResolvers();
	const finalizeGate = Promise.withResolvers();
	if (!options.holdPut) {
		putGate.resolve();
	}
	if (!options.holdFinalize) {
		finalizeGate.resolve();
	}
	const expires_at = '2099-01-01T01:00:00Z';
	const reservation_expires_at = options.reservationExpires ?? '2099-01-01T00:15:00Z';
	const metadata = () => ({ public_key: publicKey, fingerprint, expires_at });
	const reply = (route, json, status = 200) => route.fulfill({ status, json });
	const expire = () => {
		if (state === 'uploading' && Date.now() >= Date.parse(reservation_expires_at)) {
			generation++;
			state = 'waiting';
			envelope = undefined;
		}
	};
	function reserve(route) {
		const body = route.request().postDataJSON();
		reservedBodies.push(route.request().postData());
		if (body.generation !== generation) {
			return reply(route, {}, 404);
		}
		if (state === 'submitted') {
			return reply(route, { generation, state: 'submitted' });
		}
		envelope = body;
		state = 'uploading';
		if (lostReserve) {
			lostReserve = false;
			return route.abort('failed');
		}
		return reply(route, {
			generation,
			state,
			reservation_expires_at,
			upload: {
				url: `${baseUrl}/synthetic-request-upload`,
				method: 'PUT',
				expires_at: reservation_expires_at,
				headers: { 'Content-Length': String(body.size_bytes + 16) }
			}
		});
	}
	async function finalize(route) {
		const body = route.request().postDataJSON();
		finalizeStarted.resolve();
		await finalizeGate.promise;
		if (body.generation !== generation || state !== 'uploading') {
			return reply(route, {}, 409);
		}
		assert.equal(
			createHash('sha256').update(ciphertext).digest('base64'),
			envelope.ciphertext_sha256
		);
		const { kind, size_bytes, envelope: encryptedEnvelope } = envelope;
		accepted = {
			kind,
			size_bytes,
			envelope: encryptedEnvelope,
			ciphertext: ciphertext.toString('base64')
		};
		state = 'submitted';
		return options.lostFinalize ? route.abort('failed') : reply(route, { generation, state });
	}
	await context.route('**/synthetic-request-upload', async (route) => {
		const request = route.request();
		ciphertext = request.postDataBuffer();
		uploads.push(ciphertext);
		assert.equal(request.headers().authorization, undefined);
		putStarted.resolve();
		await putGate.promise;
		// A held transport may already be aborted by the browser's cancellation.
		await route.fulfill({ status: 200, body: '' }).catch(() => undefined);
	});
	await context.route('**/api/**', (route) => {
		expire();
		const request = route.request();
		const path = new URL(request.url()).pathname;
		calls.push({ path, body: request.postData() ?? '', headers: request.headers() });
		const handlers = {
			config: () =>
				reply(route, { payload_inline_max_bytes: 64, max_file_bytes: options.disabled ? 64 : 128 }),
			requests: () => {
				publicKey = request.postDataJSON().public_key;
				fingerprint = createHash('sha256')
					.update(Buffer.from(publicKey, 'base64'))
					.digest('base64url');
				return reply(
					route,
					{
						id: 'large-fixture',
						expires_at,
						submission_token: submitToken,
						retrieval_token: receiveToken
					},
					201
				);
			},
			owner: () => reply(route, { ...metadata(), state }),
			upload: () => reserve(route),
			finalize: () => finalize(route),
			attempt: () => {
				if (request.postDataJSON().generation !== generation) {
					return reply(route, {}, 404);
				}
				return reply(route, { generation, state: state === 'submitted' ? 'accepted' : state });
			},
			abandon: () => {
				assert.equal(request.postDataJSON().generation, generation);
				generation++;
				state = 'waiting';
				envelope = undefined;
				if (lostAbandon) {
					lostAbandon = false;
					return route.abort('failed');
				}
				return reply(route, { generation, state });
			},
			submit: () => {
				const {
					generation: unusedGeneration,
					attempt_token: unusedToken,
					...data
				} = request.postDataJSON();
				assert.equal(unusedGeneration, generation);
				assert.ok(unusedToken);
				accepted = data;
				state = 'submitted';
				return reply(route, { generation, state });
			},
			open: () => {
				state = 'consumed';
				return reply(route, accepted);
			}
		};
		const handle = handlers[path.split('/').at(-1)];
		return handle
			? handle()
			: reply(route, { ...metadata(), generation, can_submit: state === 'waiting' });
	});
	return { calls, reservedBodies, uploads, putStarted, putGate, finalizeStarted, finalizeGate };
}
async function pages(context) {
	const owner = await context.newPage();
	const sender = await context.newPage();
	await owner.goto(`${baseUrl}/request`);
	await owner.getByRole('button', { name: 'Create request', exact: true }).click();
	await sender.goto(await owner.getByLabel('Submission link', { exact: true }).inputValue());
	await sender.getByRole('radio', { name: 'File', exact: true }).check();
	return { owner, sender };
}
async function submitFile(sender, size = 65) {
	await sender.locator('input[type="file"]').setInputFiles({
		name: 'synthetic-private-name.bin',
		mimeType: 'application/octet-stream',
		buffer: Buffer.alloc(size, 7)
	});
	await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
}

for (const size of [63, 65, 128, 129]) {
	test(`request file boundary ${size}: inline/large/maximum`, async () => {
		const context = await browser.newContext();
		try {
			const mock = await fixture(context);
			const { owner, sender } = await pages(context);
			await submitFile(sender, size);
			if (size > 128) {
				await sender
					.getByText('Choose content between 1 and 128 bytes.', { exact: true })
					.waitFor();
				assert.equal(mock.reservedBodies.length, 0);
				return;
			}
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.equal(mock.reservedBodies.length, size > 64 ? 1 : 0);
			await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
			await owner.getByRole('button', { name: 'Open once', exact: true }).click();
			const link = owner.getByRole('link', {
				name: 'Download synthetic-private-name.bin',
				exact: true
			});
			await link.waitFor();
			const downloaded = owner.waitForEvent('download');
			await link.click();
			assert.deepEqual(await readFile(await (await downloaded).path()), Buffer.alloc(size, 7));
			assert.equal(JSON.stringify(mock.calls).includes('synthetic-private-name.bin'), false);
		} finally {
			await context.close();
		}
	});
}

test('PUT completion waits at verification and owner uploading status cannot open', async () => {
	const context = await browser.newContext();
	try {
		const mock = await fixture(context, { holdFinalize: true });
		const { owner, sender } = await pages(context);
		await submitFile(sender);
		await mock.finalizeStarted.promise;
		await sender
			.getByText('Verifying upload… Acceptance is not confirmed yet.', { exact: true })
			.waitFor();
		assert.equal(
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).count(),
			0
		);
		await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
		await owner.getByText('A file is uploading.', { exact: false }).waitFor();
		assert.equal(await owner.getByRole('button', { name: 'Open once', exact: true }).count(), 0);
		mock.finalizeGate.resolve();
		await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
	} finally {
		await context.close();
	}
});

for (const loss of ['reserve', 'finalize']) {
	test(`lost ${loss} response keeps exact ciphertext and attempts`, async () => {
		const context = await browser.newContext();
		try {
			const mock = await fixture(context, {
				lostReserve: loss === 'reserve',
				lostFinalize: loss === 'finalize'
			});
			const { sender } = await pages(context);
			await submitFile(sender);
			await sender.getByText('Submission outcome unknown.', { exact: true }).waitFor();
			await sender.getByRole('button', { name: 'Check submission', exact: true }).click();
			if (loss === 'reserve') {
				await sender.getByRole('button', { name: 'Retry same submission', exact: true }).click();
			}
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.ok(mock.reservedBodies.every((body) => body === mock.reservedBodies[0]));
			assert.equal(mock.uploads.length, 1);
		} finally {
			await context.close();
		}
	});
}

test('abort and lost abandonment require a verified new generation before the form returns', async () => {
	const context = await browser.newContext();
	try {
		const mock = await fixture(context, { holdPut: true, lostAbandon: true });
		const { sender } = await pages(context);
		await submitFile(sender);
		await mock.putStarted.promise;
		await sender.getByRole('button', { name: 'Cancel upload', exact: true }).click();
		await sender.getByText('Cancellation was not confirmed.', { exact: false }).waitFor();
		assert.equal(await sender.locator('input[type="file"]').count(), 0);
		await sender.getByRole('button', { name: 'Check submission', exact: true }).click();
		await sender.locator('input[type="file"]').waitFor();
		mock.putGate.resolve();
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/finalize')).length, 0);
		assert.equal(mock.reservedBodies.length, 1);
	} finally {
		await context.close();
	}
});

test('disabled storage enforces inline ceiling before any reservation', async () => {
	const context = await browser.newContext();
	try {
		const mock = await fixture(context, { disabled: true });
		const { sender } = await pages(context);
		await submitFile(sender);
		await sender.getByText('Choose content between 1 and 64 bytes.', { exact: true }).waitFor();
		assert.equal(mock.reservedBodies.length, 0);
	} finally {
		await context.close();
	}
});

test('reservation expiry returns to the form only after a newer server generation is observed', async () => {
	const context = await browser.newContext();
	let mock;
	try {
		mock = await fixture(context, {
			holdPut: true,
			reservationExpires: new Date(Date.now() + 5000).toISOString()
		});
		const { sender } = await pages(context);
		await submitFile(sender);
		await mock.putStarted.promise;
		await sender.getByText('The upload reservation expired.', { exact: false }).waitFor();
		assert.equal(await sender.locator('input[type="file"]').count(), 0);
		await sender.getByRole('button', { name: 'Check submission', exact: true }).click();
		await sender.locator('input[type="file"]').waitFor();
		assert.equal(mock.reservedBodies.length, 1);
		assert.equal(mock.calls.filter((call) => call.path.endsWith('/finalize')).length, 0);
	} finally {
		mock?.putGate.resolve();
		await context.close();
	}
});
