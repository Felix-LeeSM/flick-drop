import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const notice = /This browser is too old for Flick/;
let browser;
before(async () => {
	browser = await chromium.launch();
});
after(async () => {
	await browser?.close();
});

// app.html의 인라인 검사는 Tailwind 4가 기대는 @property(CSSPropertyRule)와 color-mix 둘 다 본다.
const missingFeatures = {
	'CSS @property': () => {
		delete window.CSSPropertyRule;
	},
	'color-mix()': () => {
		const supports = CSS.supports.bind(CSS);
		CSS.supports = (...args) => !String(args.join(' ')).includes('color-mix') && supports(...args);
	}
};
for (const [feature, remove] of Object.entries(missingFeatures)) {
	test(`a browser without ${feature} is told to update before opening anything`, async () => {
		const page = await browser.newPage();
		try {
			await page.addInitScript(remove);
			await page.goto(`${baseUrl}/s/anything`);
			await page.getByRole('alert').filter({ hasText: notice }).waitFor();
		} finally {
			await page.close();
		}
	});
}

test('a current browser sees no unsupported-browser notice', async () => {
	const page = await browser.newPage();
	try {
		await page.goto(`${baseUrl}/`);
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		assert.equal(await page.getByText(notice).count(), 0);
	} finally {
		await page.close();
	}
});
