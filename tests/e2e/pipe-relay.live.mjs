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
// header has no code trigger anymore) and connects with an optional passphrase.
// The code must be typed last: the modal auto-connects on the 6th digit, and the
// passphrase drawer only exists in the input state.
async function receiveManually(receiverPage, code, passphrase) {
	await receiverPage.getByRole('button', { name: 'Send with code' }).click();
	await receiverPage.getByRole('button', { name: 'Enter it here' }).click();
	await receiverPage.getByText('Connect to sender').waitFor();
	if (passphrase !== '') {
		await receiverPage.getByRole('button', { name: 'Has an additional passphrase?' }).click();
		// #pipe-passphrase: the label text also matches the create form's field
		// underneath the modal, so locate by the modal input's own id.
		await receiverPage.locator('#pipe-passphrase').fill(passphrase);
	}
	await receiverPage.getByLabel('6-digit transfer code').fill(code);
}

test('confirmation ON: manual code entry with passphrase, sender approves, text round-trips', async () => {
	const message = 'pipe e2e on-approval payload';
	const passphrase = 'pipe-e2e-passphrase';

	const senderContext = await browser.newContext();
	const sender = await senderContext.newPage();
	sender.setDefaultTimeout(20_000);
	await openHome(sender);
	await sender.getByLabel('Message').fill(message);
	// Passphrase ships OFF and lives on the link tab; the code flow reuses it
	// (the pipe key derives from code + salt + passphrase), so set it there first.
	await sender.getByRole('checkbox', { name: 'Protect with an additional passphrase' }).click();
	await sender.getByLabel('Additional passphrase', { exact: true }).fill(passphrase);
	await sender.getByRole('button', { name: 'Send with code' }).click();
	await sender.getByRole('checkbox', { name: 'Require confirmation before transfer' }).click();
	await sender.getByRole('button', { name: 'Start live transfer' }).click();
	await sender.getByText('Enter this code on receiving device').waitFor();
	const code = await readSenderCode(sender);

	// Second device: enter the code by hand through the code tab's sublink.
	const receiverContext = await browser.newContext();
	const receiver = await receiverContext.newPage();
	receiver.setDefaultTimeout(20_000);
	await openHome(receiver);
	await receiveManually(receiver, code, passphrase);

	await sender.getByText('Confirm live transfer').waitFor();
	const senderMatch = (await sender.locator('div.font-mono.text-3xl').first().textContent())?.trim();
	const receiverMatch = (await receiver.locator('div.font-mono.text-4xl').first().textContent())?.trim();
	assert.match(senderMatch ?? '', /^\d{3}$/);
	assert.equal(senderMatch, receiverMatch);

	await sender.getByRole('button', { name: 'Approve & send' }).click();
	await receiver.getByText('Secret decrypted directly in memory').waitFor();
	const received = await receiver.getByLabel('Decrypted secret content').inputValue();
	assert.equal(received, message);
	await sender.getByText('Transfer complete!').waitFor();

	await senderContext.close();
	await receiverContext.close();
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
	await receiver.getByText("Check sender's screen").waitFor();

	await receiver.getByText('Secret decrypted directly in memory').waitFor();
	const received = await receiver.getByLabel('Decrypted secret content').inputValue();
	assert.equal(received, message);
	await sender.getByText('Transfer complete!').waitFor();

	await senderContext.close();
	await receiverContext.close();
});
