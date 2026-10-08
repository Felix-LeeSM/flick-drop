// biome-ignore-all lint/style/useNamingConvention: synthetic request fixtures use API names.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: helpers execute only inside awaited tests.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const apiUrl = process.env.FLICK_API_URL ?? 'http://127.0.0.1:8080';
const disabled = process.env.FLICK_REQUEST_STORAGE_DISABLED === '1';
const vectors = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/request-crypto-v1.json', import.meta.url))
);
let browser;
let limits;
before(async () => {
	browser = await chromium.launch();
	const context = await browser.newContext();
	try {
		limits = await (await context.request.get(`${apiUrl}/api/config`)).json();
		if (disabled) {
			assert.ok(limits.max_file_bytes <= limits.payload_inline_max_bytes);
			return;
		}
		assert.ok(
			limits.max_file_bytes > limits.payload_inline_max_bytes && limits.max_file_bytes <= 2_097_152,
			'Use isolated API/MinIO with FLICK_PAYLOAD_INLINE_MAX_BYTES=65536 and FLICK_MAX_FILE_BYTES=131072'
		);
	} finally {
		await context.close();
	}
});
after(async () => {
	await browser?.close();
});
async function create(owner, sender) {
	await owner.goto(`${baseUrl}/request`);
	await owner.getByRole('button', { name: 'Create request', exact: true }).click();
	const submit = await owner.getByLabel('Submission link', { exact: true }).inputValue();
	const receive = await owner.getByLabel('Private retrieval link', { exact: true }).inputValue();
	await sender.goto(submit);
	await sender.getByRole('radio', { name: 'File', exact: true }).check();
	return {
		submit,
		receive,
		id: new URL(submit).pathname.split('/').at(-1),
		token: new URLSearchParams(new URL(submit).hash.slice(1)).get('submit'),
		privateKey: new URLSearchParams(new URL(receive).hash.slice(1)).get('key')
	};
}
async function sendFile(sender, size) {
	await sender.locator('input[type="file"]').setInputFiles({
		name: 'synthetic-request-private.bin',
		mimeType: 'application/octet-stream',
		buffer: Buffer.alloc(size, 17)
	});
	await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
}
function request(context, links, action, data) {
	return context.request.fetch(`${apiUrl}/api/requests/${links.id}${action}`, {
		method: action ? 'POST' : 'GET',
		headers: { authorization: `Bearer ${links.token}` },
		...(data ? { data } : {})
	});
}

for (const boundary of disabled
	? ['above-maximum']
	: ['below-inline', 'above-inline', 'maximum', 'above-maximum']) {
	test(`real API and MinIO: ${boundary} with separate submitter/requester contexts`, async () => {
		const ownerContext = await browser.newContext();
		const senderContext = await browser.newContext();
		try {
			const owner = await ownerContext.newPage();
			const sender = await senderContext.newPage();
			const calls = [];
			for (const context of [ownerContext, senderContext]) {
				context.on('request', (value) => {
					calls.push({ url: value.url(), headers: value.headers(), body: value.postData() ?? '' });
				});
			}
			const links = await create(owner, sender);
			const sizes = {
				'below-inline': limits.payload_inline_max_bytes - 1,
				'above-inline': limits.payload_inline_max_bytes + 1,
				maximum: limits.max_file_bytes,
				'above-maximum': limits.max_file_bytes + 1
			};
			const size = sizes[boundary];
			await sendFile(sender, size);
			if (boundary === 'above-maximum') {
				await sender
					.getByText(
						`Choose content between 1 and ${limits.max_file_bytes.toLocaleString()} bytes.`,
						{ exact: true }
					)
					.waitFor();
				assert.equal(
					calls.filter((call) => call.url.endsWith('/upload') || call.url.endsWith('/submit'))
						.length,
					0
				);
				return;
			}
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.equal(
				calls.filter((call) => call.url.endsWith('/upload')).length,
				boundary === 'below-inline' ? 0 : 1
			);
			await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
			await owner.getByRole('button', { name: 'Open once', exact: true }).click();
			const download = owner.getByRole('link', {
				name: 'Download synthetic-request-private.bin',
				exact: true
			});
			await download.waitFor();
			const downloaded = owner.waitForEvent('download');
			await download.click();
			assert.deepEqual(await readFile(await (await downloaded).path()), Buffer.alloc(size, 17));
			assert.equal(JSON.stringify(calls).includes(links.privateKey), false);
			assert.equal(JSON.stringify(calls).includes('synthetic-request-private.bin'), false);
			await owner.reload();
			await owner.getByRole('heading', { name: 'Consumed', exact: true }).waitFor();
		} finally {
			await ownerContext.close();
			await senderContext.close();
		}
	});
}

if (!disabled) {
	test('real API and MinIO: inline competitor loses reservation, abort advances generation, late PUT cannot finalize', async () => {
		const ownerContext = await browser.newContext();
		const senderContext = await browser.newContext();
		const gate = Promise.withResolvers();
		try {
			const owner = await ownerContext.newPage();
			const sender = await senderContext.newPage();
			const links = await create(owner, sender);
			const captured = Promise.withResolvers();
			await sender.route('**/managed/requests/**', async (route) => {
				captured.resolve(route.request().postDataBuffer());
				await gate.promise;
				await route.abort('failed').catch(() => undefined);
			});
			const reservationResponse = sender.waitForResponse('**/upload');
			const reservationRequest = sender.waitForRequest('**/upload');
			await sendFile(sender, limits.payload_inline_max_bytes + 1);
			const reserved = await (await reservationResponse).json();
			const attempt = (await reservationRequest).postDataJSON();
			const ciphertext = await captured.promise;
			const competitor = await senderContext.newPage();
			await competitor.goto(links.submit);
			await competitor.getByRole('heading', { name: 'Request unavailable', exact: true }).waitFor();
			// Instructions already fence ordinary UI contenders; direct inline contention uses valid wire metadata.
			const conflict = await request(senderContext, links, '/submit', {
				...vectors.file.payload,
				generation: attempt.generation,
				attempt_token: attempt.attempt_token
			});
			assert.equal(conflict.status(), 409);
			await sender.getByRole('button', { name: 'Cancel upload', exact: true }).click();
			await sender.locator('input[type="file"]').waitFor();
			const next = await (await request(senderContext, links, '')).json();
			assert.ok(next.generation > attempt.generation);
			gate.resolve();
			const latePut = await senderContext.request.fetch(reserved.upload.url, {
				method: reserved.upload.method,
				headers: reserved.upload.headers,
				data: ciphertext
			});
			assert.ok(latePut.ok(), 'Synthetic late PUT should still be valid before signed expiry');
			const finalize = await request(senderContext, links, '/finalize', {
				generation: attempt.generation,
				attempt_token: attempt.attempt_token
			});
			assert.equal(finalize.status(), 404);
			assert.equal((await (await request(senderContext, links, '')).json()).can_submit, true);
		} finally {
			gate.resolve();
			await ownerContext.close();
			await senderContext.close();
		}
	});

	test('real API and MinIO: lost committed finalize is recovered by attempt receipt without another PUT', async () => {
		const ownerContext = await browser.newContext();
		const senderContext = await browser.newContext();
		try {
			const owner = await ownerContext.newPage();
			const sender = await senderContext.newPage();
			await create(owner, sender);
			let puts = 0;
			sender.on('request', (value) => {
				if (value.method() === 'PUT') {
					puts++;
				}
			});
			await sender.route('**/finalize', async (route) => {
				assert.ok((await route.fetch()).ok());
				await route.abort('failed').catch(() => undefined);
			});
			await sendFile(sender, limits.payload_inline_max_bytes + 1);
			await sender.getByText('Submission outcome unknown.', { exact: true }).waitFor();
			await sender.getByRole('button', { name: 'Check submission', exact: true }).click();
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			assert.equal(puts, 1);
		} finally {
			await ownerContext.close();
			await senderContext.close();
		}
	});
}
