// Needs a running dev stack (scripts/dev/up.sh); each browser context is a separate device.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

// Must be localhost, not 127.0.0.1: the API's CORS origin from up.sh.
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://localhost:5173';
let browser;

before(async () => {
	browser = await chromium.launch();
});

after(async () => {
	await browser?.close();
});

async function readSenderCode(senderPage) {
	const label = await senderPage.getByLabel(/Transfer code/).getAttribute('aria-label');
	return label.replace('Transfer code ', '').replaceAll('-', '');
}

// A click before hydration is lost silently; /api/config (fired from onMount) is the barrier.
async function openHome(page) {
	await Promise.all([
		page.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
		page.goto(baseUrl)
	]);
}

// Auto-connects on the 6th digit; a code transfer takes no passphrase.
async function receiveManually(receiverPage, code) {
	await receiverPage.getByRole('button', { name: 'Live transfer' }).click();
	await receiverPage.getByRole('button', { name: 'Enter it here' }).click();
	await receiverPage.getByText('Connect to sender').waitFor();
	await receiverPage.getByLabel('6-digit transfer code').fill(code);
}

async function readOwnNumber(receiverPage) {
	const label = await receiverPage
		.getByRole('img', { name: /^Your number/ })
		.getAttribute('aria-label');
	return label.replace('Your number ', '').replaceAll(' ', '');
}

test('waiting room: two devices wait, sender denies one and sends to the number read aloud', async () => {
	const message = 'pipe e2e waiting-room payload';

	const senderContext = await browser.newContext();
	const sender = await senderContext.newPage();
	sender.setDefaultTimeout(20_000);
	await openHome(sender);
	await sender.getByLabel('Message').fill(message);
	// A link-tab passphrase must not reach the code transfer.
	await sender.getByRole('switch', { name: 'Protect with a passphrase' }).click();
	await sender.getByLabel('Passphrase', { exact: true }).fill('link-tab-only');
	await sender.getByRole('button', { name: 'Live transfer' }).click();
	await sender.getByRole('button', { name: 'Start live transfer' }).click();
	await sender.getByText('Live transfer code', { exact: true }).waitFor();
	const code = await readSenderCode(sender);

	const receiverContext = await browser.newContext();
	const receiver = await receiverContext.newPage();
	receiver.setDefaultTimeout(20_000);
	await openHome(receiver);
	await receiveManually(receiver, code);

	await receiver.getByText('Read this to the sender').waitFor();
	const receiverNumber = await readOwnNumber(receiver);
	assert.match(receiverNumber, /^\d{3}$/);

	const otherContext = await browser.newContext();
	const other = await otherContext.newPage();
	other.setDefaultTimeout(20_000);
	await openHome(other);
	await receiveManually(other, code);
	await other.getByText('Read this to the sender').waitFor();
	const otherNumber = await readOwnNumber(other);
	assert.notEqual(otherNumber, receiverNumber);

	await sender.getByText('2 of 10 devices waiting').waitFor();
	// Rows list in join order.
	await sender.getByRole('button', { name: 'Deny connection 2' }).click();
	await other.getByText('The sender declined this connection').waitFor();
	await sender.getByText('1 of 10 devices waiting').waitFor();

	await sender
		.getByRole('button', { name: `Send to connection 1, number ${receiverNumber}` })
		.click();
	await receiver.getByRole('heading', { name: 'Secret', exact: true }).waitFor();
	const received = (await receiver.getByLabel('Received content').textContent())?.trim();
	assert.equal(received, message);
	await sender.getByRole('heading', { name: 'Transfer complete', exact: true }).waitFor();

	await senderContext.close();
	await receiverContext.close();
	await otherContext.close();
});

test('#code deep link auto-joins into waiting-room confirmation without a passphrase', async () => {
	const message = 'pipe e2e deep-link payload';

	const senderContext = await browser.newContext();
	const sender = await senderContext.newPage();
	sender.setDefaultTimeout(20_000);
	await openHome(sender);
	await sender.getByLabel('Message').fill(message);
	await sender.getByRole('button', { name: 'Live transfer' }).click();
	await sender.getByRole('button', { name: 'Start live transfer' }).click();
	await sender.getByText('Live transfer code', { exact: true }).waitFor();
	const code = await readSenderCode(sender);

	const receiverContext = await browser.newContext();
	const receiver = await receiverContext.newPage();
	receiver.setDefaultTimeout(20_000);
	await Promise.all([
		receiver.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
		receiver.goto(`${baseUrl}/#code=${code}`)
	]);
	await receiver.getByText('Read this to the sender').waitFor();
	const receiverNumber = await readOwnNumber(receiver);
	await sender
		.getByRole('button', { name: `Send to connection 1, number ${receiverNumber}` })
		.click();
	await receiver.getByRole('heading', { name: 'Secret', exact: true }).waitFor();
	const received = (await receiver.getByLabel('Received content').textContent())?.trim();
	assert.equal(received, message);
	await sender.getByRole('heading', { name: 'Transfer complete', exact: true }).waitFor();

	await senderContext.close();
	await receiverContext.close();
});
