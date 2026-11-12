// Cross-device live relay ("Send with code") E2E against a running dev stack.
// Start the stack first: scripts/dev/up.sh (NATS + MinIO + api :8080 + web :5173).
// Each browser context simulates a separate device.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

// The web origin must match the API's allowed CORS origin, which up.sh sets to
// localhost — open the page at localhost, not 127.0.0.1.
const baseUrl = process.env.FLICK_WEB_URL ?? 'http://localhost:5173';
let browser;

before(async () => {
	browser = await chromium.launch();
});

after(async () => {
	await browser?.close();
});

// Reads the 6-digit code from the sender's waiting panel (aria-label carries "482-913").
async function readSenderCode(senderPage) {
	const label = await senderPage.getByLabel(/Transfer code/).getAttribute('aria-label');
	return label.replace('Transfer code ', '').replaceAll('-', '');
}

// Navigating home is only safe once the Svelte app has hydrated — a click before
// that is lost silently (known cold-start race). /api/config fires from onMount,
// so its response doubles as the hydration barrier.
async function openHome(page) {
	await Promise.all([
		page.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
		page.goto(baseUrl)
	]);
}

// Opens the receive modal from the code tab's "Enter it here" sublink (the
// header has no code trigger anymore) and types the code; the modal
// auto-connects on the 6th digit. A code transfer takes no passphrase: the
// stream is encrypted to the receiving browser's own age recipient.
async function receiveManually(receiverPage, code) {
	await receiverPage.getByRole('button', { name: 'Send with code' }).click();
	await receiverPage.getByRole('button', { name: 'Enter it here' }).click();
	await receiverPage.getByText('Connect to sender').waitFor();
	await receiverPage.getByLabel('6-digit transfer code').fill(code);
}

// Reads the receiver's own 3-digit number (aria-label "Your number 2 7 1").
async function readOwnNumber(receiverPage) {
	const label = await receiverPage.getByRole('img', { name: /^Your number/ }).getAttribute('aria-label');
	return label.replace('Your number ', '').replaceAll(' ', '');
}

test('confirmation ON: two devices wait, sender denies one and sends to the number read aloud', async () => {
	const message = 'pipe e2e on-approval payload';

	const senderContext = await browser.newContext();
	const sender = await senderContext.newPage();
	sender.setDefaultTimeout(20_000);
	await openHome(sender);
	await sender.getByLabel('Message').fill(message);
	// A passphrase left on in the link tab must not reach the code transfer: the
	// receivers below enter none and the chosen one still decrypts.
	await sender.getByRole('switch', { name: 'Protect with an additional passphrase' }).click();
	await sender.getByLabel('Additional passphrase', { exact: true }).fill('link-tab-only');
	await sender.getByRole('button', { name: 'Send with code' }).click();
	await sender.getByRole('switch', { name: 'Require confirmation before transfer' }).click();
	await sender.getByRole('button', { name: 'Start live transfer' }).click();
	await sender.getByText('Enter this code on receiving device').waitFor();
	const code = await readSenderCode(sender);

	// Second device: enter the code by hand through the code tab's sublink.
	const receiverContext = await browser.newContext();
	const receiver = await receiverContext.newPage();
	receiver.setDefaultTimeout(20_000);
	await openHome(receiver);
	await receiveManually(receiver, code);

	await receiver.getByText('Read this to the sender').waitFor();
	const receiverNumber = await readOwnNumber(receiver);
	assert.match(receiverNumber, /^\d{3}$/);

	// A second device enters the same code and gets its own, different number.
	const otherContext = await browser.newContext();
	const other = await otherContext.newPage();
	other.setDefaultTimeout(20_000);
	await openHome(other);
	await receiveManually(other, code);
	await other.getByText('Read this to the sender').waitFor();
	const otherNumber = await readOwnNumber(other);
	assert.notEqual(otherNumber, receiverNumber);

	await sender.getByText('2 of 10 devices waiting').waitFor();
	// Rows list in join order: connection 1 is the receiver, connection 2 the other device.
	await sender.getByRole('button', { name: 'Deny connection 2' }).click();
	await other.getByText('The sender declined this device').waitFor();
	await sender.getByText('1 of 10 devices waiting').waitFor();

	await sender.getByRole('button', { name: `Send to connection 1, number ${receiverNumber}` }).click();
	await receiver.getByText('Secret decrypted directly in memory').waitFor();
	const received = await receiver.getByLabel('Decrypted secret content').inputValue();
	assert.equal(received, message);
	await sender.getByText('Transfer complete!').waitFor();

	await senderContext.close();
	await receiverContext.close();
	await otherContext.close();
});

test('confirmation OFF: #code deep link auto-receives without approval or passphrase', async () => {
	const message = 'pipe e2e no-approval payload';

	const senderContext = await browser.newContext();
	const sender = await senderContext.newPage();
	sender.setDefaultTimeout(20_000);
	await openHome(sender);
	await sender.getByLabel('Message').fill(message);
	// Both toggles ship OFF now — nothing to untoggle.
	await sender.getByRole('button', { name: 'Send with code' }).click();
	await sender.getByRole('button', { name: 'Start live transfer' }).click();
	await sender.getByText('Enter this code on receiving device').waitFor();
	const statusText = await sender.locator('span', { hasText: /Waiting for receiver/ }).first().textContent();
	assert.ok(!statusText?.includes('confirmation required'), 'status should not advertise confirmation');
	const code = await readSenderCode(sender);

	// Second device: the #code= deep link prefills the modal and auto-connects.
	const receiverContext = await browser.newContext();
	const receiver = await receiverContext.newPage();
	receiver.setDefaultTimeout(20_000);
	await Promise.all([
		receiver.waitForResponse((response) => new URL(response.url()).pathname === '/api/config'),
		receiver.goto(`${baseUrl}/#code=${code}`)
	]);
	await receiver.getByText('Secret decrypted directly in memory').waitFor();
	const received = await receiver.getByLabel('Decrypted secret content').inputValue();
	assert.equal(received, message);
	await sender.getByText('Transfer complete!').waitFor();

	await senderContext.close();
	await receiverContext.close();
});
