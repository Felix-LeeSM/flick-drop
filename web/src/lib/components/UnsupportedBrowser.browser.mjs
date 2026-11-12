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

// app.html의 인라인 검사는 Tailwind 4가 기대는 @property(CSSPropertyRule) 유무로 판단한다.
test('a browser without CSS @property is told to update before opening anything', async () => {
	const page = await browser.newPage();
	try {
		// Given @property가 없는 오래된 브라우저
		await page.addInitScript(() => {
			delete window.CSSPropertyRule;
		});
		// When 아무 페이지나 열면
		await page.goto(`${baseUrl}/s/anything`);
		// Then 맨 위에 업데이트 안내가 뜬다
		await page.getByRole('alert').filter({ hasText: notice }).waitFor();
	} finally {
		await page.close();
	}
});

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
