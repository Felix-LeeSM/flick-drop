// biome-ignore-all lint/style/useNamingConvention: synthetic request fixtures use canonical API fields.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: Node test helpers are called only by awaited tests.
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

// Run only against an isolated API/database. See docs/architecture/ci-testing.md.
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const apiUrl = process.env.FLICK_API_URL ?? 'http://127.0.0.1:8080';
const vectors = JSON.parse(
	await readFile(new URL('../../../../tests/fixtures/request-crypto-v1.json', import.meta.url))
);
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

async function create(owner) {
	await owner.goto(`${baseUrl}/request`);
	await owner.getByRole('button', { name: 'Create request', exact: true }).click();
	await owner.waitForURL('**/receive#receive=*');
	const submit = await owner.getByLabel('Submission link', { exact: true }).inputValue();
	const receive = await owner.getByLabel('Private retrieval link', { exact: true }).inputValue();
	return {
		submit,
		receive,
		id: new URL(submit).pathname.split('/').at(-1),
		submitToken: new URLSearchParams(new URL(submit).hash.slice(1)).get('submit'),
		receiveToken: new URLSearchParams(new URL(receive).hash.slice(1)).get('receive'),
		key: new URLSearchParams(new URL(receive).hash.slice(1)).get('key')
	};
}
function authorized(context, id, action, token, data) {
	return context.request.fetch(`${apiUrl}/api/requests/${id}${action}`, {
		method: action === '' || action === '/owner' ? 'GET' : 'POST',
		headers: { authorization: `Bearer ${token}` },
		...(data ? { data } : {})
	});
}
function capture(context, calls) {
	context.on('request', (request) => {
		if (new URL(request.url()).pathname.startsWith('/api/')) {
			calls.push({
				url: request.url(),
				headers: request.headers(),
				body: request.postData() ?? ''
			});
		}
	});
}
async function noKeyPersistence(page, key) {
	assert.equal(
		await page.evaluate(
			(value) =>
				JSON.stringify({
					history: history.state,
					local: { ...localStorage },
					session: { ...sessionStorage }
				}).includes(value),
			key
		),
		false
	);
	assert.deepEqual(await page.evaluate(() => indexedDB.databases()), []);
}

for (const kind of ['text', 'file']) {
	test(`real API: ${kind}, separate submitter, reload/new-device private link and one-time release`, async () => {
		const ownerContext = await browser.newContext({ viewport: { width: 375, height: 812 } });
		const senderContext = await browser.newContext();
		const resumedContext = await browser.newContext();
		try {
			const calls = [];
			for (const context of [ownerContext, senderContext, resumedContext]) {
				capture(context, calls);
			}
			const owner = await ownerContext.newPage();
			const sender = await senderContext.newPage();
			const links = await create(owner);
			assert.equal(
				(await authorized(senderContext, links.id, '/owner', links.submitToken)).status(),
				404
			);
			assert.equal(
				(await authorized(senderContext, links.id, '/open', links.submitToken)).status(),
				404
			);
			assert.equal(
				(await authorized(senderContext, links.id, '/revoke', links.submitToken)).status(),
				404
			);
			assert.equal(
				(await authorized(ownerContext, links.id, '', links.receiveToken)).status(),
				404
			);
			await owner.reload();
			await owner.getByText('Waiting for the first submission.', { exact: true }).waitFor();
			assert.equal(await owner.getByLabel('Submission link', { exact: true }).count(), 0);
			await sender.goto(links.submit);
			const plaintext = `Synthetic request ${kind} bytes 안녕하세요`;
			if (kind === 'text') {
				await sender.getByLabel('Message', { exact: true }).fill(plaintext);
			} else {
				await sender.getByRole('radio', { name: 'File', exact: true }).check();
				await sender.locator('input[type="file"]').setInputFiles({
					name: 'synthetic-private-filename.txt',
					mimeType: 'text/plain',
					buffer: Buffer.from(plaintext)
				});
			}
			const submission = sender.waitForRequest('**/submit');
			await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
			await sender.getByRole('heading', { name: 'Submission accepted', exact: true }).waitFor();
			const submittedBody = (await submission).postDataJSON();
			const overwrite = { ...submittedBody, attempt_token: randomBytes(32).toString('base64url') };
			assert.equal(
				(
					await authorized(senderContext, links.id, '/submit', links.submitToken, overwrite)
				).status(),
				409
			);
			const resumed = await resumedContext.newPage();
			await resumed.goto(links.receive);
			await resumed.getByRole('heading', { name: 'Ready to retrieve', exact: true }).waitFor();
			assert.equal(await resumed.getByLabel('Submission link', { exact: true }).count(), 0);
			await resumed.getByRole('button', { name: 'Open once', exact: true }).click();
			if (kind === 'text') {
				assert.equal(await resumed.getByLabel('Decrypted secret').inputValue(), plaintext);
			} else {
				const download = resumed.getByRole('link', {
					name: 'Download synthetic-private-filename.txt',
					exact: true
				});
				await download.waitFor();
				const downloaded = resumed.waitForEvent('download');
				await download.click();
				assert.equal(await readFile(await (await downloaded).path(), 'utf8'), plaintext);
			}
			assert.equal(JSON.stringify(calls).includes(links.key), false);
			assert.equal(JSON.stringify(calls).includes(plaintext), false);
			assert.equal(JSON.stringify(calls).includes('synthetic-private-filename.txt'), false);
			await noKeyPersistence(owner, links.key);
			await noKeyPersistence(resumed, links.key);
			assert.equal(
				(await authorized(ownerContext, links.id, '/open', links.receiveToken)).status(),
				409
			);
			await resumed.reload();
			await resumed.getByRole('heading', { name: 'Consumed', exact: true }).waitFor();
		} finally {
			await ownerContext.close();
			await senderContext.close();
			await resumedContext.close();
		}
	});
}

test('real API: cancellation rejects a later submission and never fabricates consumption', async () => {
	const context = await browser.newContext();
	const senderContext = await browser.newContext();
	try {
		const owner = await context.newPage();
		const sender = await senderContext.newPage();
		const links = await create(owner);
		await sender.goto(links.submit);
		await sender.getByLabel('Message', { exact: true }).fill('Synthetic cancelled request');
		owner.on('dialog', (dialog) => dialog.accept());
		await owner.getByRole('button', { name: 'Cancel request', exact: true }).click();
		await owner.getByRole('heading', { name: 'Cancelled', exact: true }).waitFor();
		await sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click();
		await sender.getByText('Submission outcome unknown.', { exact: true }).waitFor();
		await sender.getByRole('button', { name: 'Check submission', exact: true }).click();
		await sender.getByRole('heading', { name: 'Request unavailable', exact: true }).waitFor();
		assert.equal((await authorized(context, links.id, '/owner', links.receiveToken)).status(), 200);
	} finally {
		await context.close();
		await senderContext.close();
	}
});

test('real API: concurrent submitters and requester tabs each have only one winner', async () => {
	const ownerContext = await browser.newContext();
	const senderContext = await browser.newContext();
	try {
		const owner = await ownerContext.newPage();
		const links = await create(owner);
		const senders = await Promise.all([senderContext.newPage(), senderContext.newPage()]);
		for (const [index, sender] of senders.entries()) {
			await sender.goto(links.submit);
			await sender.getByLabel('Message', { exact: true }).fill(`Synthetic race ${index}`);
		}
		const responses = senders.map((sender) => sender.waitForResponse('**/submit'));
		await Promise.all(
			senders.map((sender) =>
				sender.getByRole('button', { name: 'Encrypt and submit', exact: true }).click()
			)
		);
		assert.deepEqual(
			(await Promise.all(responses)).map((response) => response.status()).sort(),
			[200, 409]
		);
		const otherOwner = await ownerContext.newPage();
		await otherOwner.goto(links.receive);
		await owner.getByRole('button', { name: 'Refresh status', exact: true }).click();
		for (const page of [owner, otherOwner]) {
			await page.getByRole('button', { name: 'Open once', exact: true }).waitFor();
		}
		const opens = [owner.waitForResponse('**/open'), otherOwner.waitForResponse('**/open')];
		await Promise.all(
			[owner, otherOwner].map((page) =>
				page.getByRole('button', { name: 'Open once', exact: true }).click()
			)
		);
		assert.deepEqual(
			(await Promise.all(opens)).map((response) => response.status()).sort(),
			[200, 409]
		);
	} finally {
		await ownerContext.close();
		await senderContext.close();
	}
});

test('real API: original deadline expires owner and submission authority', async () => {
	const context = await browser.newContext();
	try {
		// The isolated API must set FLICK_MIN_TTL_SECONDS=1; no production TTL override is used.
		const response = await context.request.post(`${apiUrl}/api/requests`, {
			data: { public_key: vectors.public_key, ttl_seconds: 3 }
		});
		assert.equal(
			response.status(),
			201,
			'Use the documented isolated API with FLICK_MIN_TTL_SECONDS=1'
		);
		const created = await response.json();
		const owner = await context.newPage();
		await owner.goto(
			`${baseUrl}/r/${created.id}/receive#receive=${created.retrieval_token}&key=${vectors.private_key}`
		);
		await owner.getByRole('heading', { name: 'Request expired', exact: true }).waitFor();
		assert.equal(
			(await authorized(context, created.id, '/owner', created.retrieval_token)).status(),
			404
		);
		assert.equal(
			(await authorized(context, created.id, '', created.submission_token)).status(),
			404
		);
	} finally {
		await context.close();
	}
});
