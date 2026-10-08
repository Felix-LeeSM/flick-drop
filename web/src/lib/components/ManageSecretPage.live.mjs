import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

for (const model of ['a', 'b']) {
	for (const operation of ['open', 'cancel']) {
		test(`real API Model ${model}: create → management → ${operation}`, async () => {
			const context = await browser.newContext();
			const sender = await context.newPage();
			try {
				const configured = sender.waitForResponse('**/api/config');
				await sender.goto(baseUrl);
				await configured;
				await sender.getByLabel('Message', { exact: true }).fill('Synthetic managed delivery');
				if (model === 'a') {
					await sender.getByLabel('Passphrase', { exact: true }).fill('synthetic-passphrase');
				} else {
					await sender.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();
				}
				await sender.getByRole('button', { name: 'Create link', exact: true }).click();
				await sender.waitForURL('**/m/*#manage=*');
				await sender.getByRole('heading', { name: 'Ready to open', exact: true }).waitFor();
				const recipientUrl = await sender.getByLabel('Share URL', { exact: true }).inputValue();
				const recipient = await context.newPage();
				if (operation === 'cancel') {
					sender.on('dialog', (dialog) => dialog.accept());
					await sender.getByRole('button', { name: 'Cancel delivery', exact: true }).click();
					await sender.getByRole('heading', { name: 'Cancelled', exact: true }).waitFor();
					await recipient.goto(recipientUrl);
					await recipient
						.getByText('This secret is no longer available.', { exact: true })
						.waitFor();
				} else {
					await recipient.goto(recipientUrl);
					if (model === 'a') {
						await recipient.getByLabel('passphrase', { exact: true }).fill('synthetic-passphrase');
					}
					await recipient.getByRole('button', { name: 'Open', exact: true }).click();
					assert.equal(
						await recipient.getByLabel('Decrypted secret').inputValue(),
						'Synthetic managed delivery'
					);
					await sender.getByRole('button', { name: 'Refresh status', exact: true }).click();
					await sender.getByRole('heading', { name: 'Opened', exact: true }).waitFor();
					await recipient.reload();
					await recipient
						.getByText('This secret is no longer available.', { exact: true })
						.waitFor();
				}
			} finally {
				await context.close();
			}
		});
	}
}
