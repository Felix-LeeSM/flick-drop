// biome-ignore-all lint/style/useNamingConvention: API fixtures use canonical wire-format names.
// biome-ignore-all lint/suspicious/noMisplacedAssertion: Node test helpers run only within awaited tests.
// Run against a local Vite server with the default 5-minute / 7-day TTL bounds.
// FLICK_BROWSER_DEFAULT_TTL_SECONDS must match an overridden build-time default.
// Every API call is intercepted; no deliveries are stored. Optional blank-form
// screenshots: FLICK_BROWSER_SCREENSHOT_DIR=/tmp/flick-create-forms.
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const baseUrl = process.env.FLICK_WEB_URL ?? 'http://127.0.0.1:5173';
const defaultTtlSeconds = Number(process.env.FLICK_BROWSER_DEFAULT_TTL_SECONDS ?? 3600);
const screenshotDir = process.env.FLICK_BROWSER_SCREENSHOT_DIR;
const forms = [
	{ name: 'sender', path: '/', group: 'Secret lifetime' },
	{ name: 'request', path: '/request', group: 'Request lifetime' }
];
const presets = [
	['10 min', 600],
	['1 hour', 3600],
	['24 hours', 86_400],
	['7 days', 604_800]
];
const typeNames = ['Text', 'File', 'Login', 'Card', 'Identity', 'Custom'];
let browser;
before(async () => {
	browser = await chromium.launch();
	if (screenshotDir) {
		await mkdir(screenshotDir, { recursive: true });
	}
});
after(async () => {
	await browser?.close();
});

async function fixture(page, hold = false) {
	const ttls = [];
	let release;
	let entered;
	const pending = new Promise((resolve) => {
		release = resolve;
	});
	const started = new Promise((resolve) => {
		entered = resolve;
	});
	await page.route(
		(url) => url.pathname.startsWith('/api/'),
		async (route) => {
			const request = route.request();
			const path = new URL(request.url()).pathname;
			if (path === '/api/config' && request.method() === 'GET') {
				return route.fulfill({
					json: { payload_inline_max_bytes: 1_048_560, max_file_bytes: 52_428_800 }
				});
			}
			assert.ok(['/api/secrets', '/api/requests'].includes(path), `Unexpected API path: ${path}`);
			assert.equal(request.method(), 'POST');
			// Retain only the lifetime: keys and ciphertext are irrelevant to this regression.
			ttls.push(request.postDataJSON().ttl_seconds);
			entered();
			if (hold) {
				await pending;
			}
			// A deterministic rejection keeps the form visible without creating private links.
			return route.fulfill({
				status: 400,
				json: { error: { code: 'invalid_request', message: 'Synthetic rejection' } }
			});
		}
	);
	return { ttls, started, release };
}

async function openForm(page, form, content = false) {
	const hydrated =
		form.name === 'sender'
			? page.waitForResponse((response) => new URL(response.url()).pathname === '/api/config')
			: undefined;
	await page.goto(`${baseUrl}${form.path}`);
	await hydrated;
	// SvelteKit emits this element only after its root onMount has completed.
	await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
	await page.getByRole('group', { name: form.group, exact: true }).waitFor();
	await page.evaluate(() => document.fonts.ready);
	if (form.name === 'sender' && content) {
		await page.getByLabel('Message', { exact: true }).fill('Synthetic lifetime regression');
		await page.getByRole('checkbox', { name: 'Protect with a passphrase' }).uncheck();
	}
}

const valueInput = (page) => page.getByLabel('Custom lifetime value', { exact: true });
const unitInput = (page) => page.getByLabel('Custom lifetime unit', { exact: true });
const submitButton = (page) => page.locator('form button[type="submit"]');

async function custom(page, value, unit) {
	await valueInput(page).fill(value);
	await unitInput(page).selectOption(unit);
}

for (const form of forms) {
	test(`${form.name} submits default, preset, and custom lifetimes in seconds`, async () => {
		const page = await browser.newPage();
		try {
			const mock = await fixture(page);
			const cases = [
				{ seconds: defaultTtlSeconds },
				...presets.map(([preset, seconds]) => ({ preset, seconds })),
				{ value: '15', unit: 'minutes', seconds: 900 },
				{ value: '3', unit: 'hours', seconds: 10_800 },
				{ value: '2', unit: 'days', seconds: 172_800 }
			];
			for (const item of cases) {
				await openForm(page, form, true);
				const group = page.getByRole('group', { name: form.group, exact: true });
				const defaultPreset = presets.find(([, seconds]) => seconds === defaultTtlSeconds);
				if (defaultPreset) {
					assert.equal(
						await group
							.getByRole('button', { name: defaultPreset[0], exact: true })
							.getAttribute('aria-pressed'),
						'true'
					);
				} else {
					assert.equal(await group.locator('button[aria-pressed="true"]').count(), 0);
					const unit = await unitInput(page).inputValue();
					const factor = { minutes: 60, hours: 3600, days: 86_400 }[unit];
					assert.equal(Number(await valueInput(page).inputValue()) * factor, defaultTtlSeconds);
					if (defaultTtlSeconds === 1800) {
						assert.equal(await valueInput(page).inputValue(), '30');
						assert.equal(unit, 'minutes');
					}
				}
				if (item.preset) {
					await group.getByRole('button', { name: item.preset, exact: true }).click();
				}
				if (item.unit) {
					await custom(page, item.value, item.unit);
				}
				const response = page.waitForResponse((reply) => reply.request().method() === 'POST');
				await submitButton(page).click();
				await response;
				assert.equal(mock.ttls.at(-1), item.seconds);
			}
			assert.equal(mock.ttls.length, cases.length);
		} finally {
			await page.close();
		}
	});

	test(`${form.name} blocks empty and out-of-range custom lifetime before any POST`, async () => {
		const page = await browser.newPage();
		try {
			const mock = await fixture(page);
			await openForm(page, form, true);
			for (const [value, unit] of [
				['', 'minutes'],
				['4', 'minutes'],
				['8', 'days']
			]) {
				await custom(page, value, unit);
				await page
					.getByRole('alert')
					.filter({ hasText: 'Choose a lifetime between 5 minutes and 7 days.' })
					.first()
					.waitFor();
				assert.equal(await valueInput(page).getAttribute('aria-invalid'), 'true');
				assert.equal(await submitButton(page).isDisabled(), true);
				// Exercise the submit guard too, independently of the disabled button.
				await page.locator('form').evaluate((form) => form.requestSubmit());
			}
			await page.waitForTimeout(300);
			assert.deepEqual(mock.ttls, []);
			await page
				.getByRole('group', { name: form.group, exact: true })
				.getByRole('button', { name: '1 hour', exact: true })
				.click();
			assert.equal(await valueInput(page).getAttribute('aria-invalid'), 'false');
			assert.equal(await submitButton(page).isEnabled(), true);
		} finally {
			await page.close();
		}
	});

	test(`${form.name} freezes lifetime controls and prevents duplicate submit while pending`, async () => {
		const page = await browser.newPage();
		const mock = await fixture(page, true);
		try {
			await openForm(page, form, true);
			await custom(page, '3', 'hours');
			await submitButton(page).click();
			await mock.started;
			assert.equal(await valueInput(page).isDisabled(), true);
			assert.equal(await unitInput(page).isDisabled(), true);
			assert.equal(await submitButton(page).isDisabled(), true);
			for (const [name] of presets) {
				assert.equal(
					await page
						.getByRole('group', { name: form.group, exact: true })
						.getByRole('button', { name, exact: true })
						.isDisabled(),
					true
				);
			}
			await page.locator('form').evaluate((form) => form.requestSubmit());
			const response = page.waitForResponse((reply) => reply.request().method() === 'POST');
			mock.release();
			await response;
			await page.waitForFunction(
				() => !document.querySelector('input[aria-label="Custom lifetime value"]').disabled
			);
			assert.equal(await unitInput(page).isEnabled(), true);
			assert.deepEqual(mock.ttls, [10_800]);
		} finally {
			mock.release();
			await page.close();
		}
	});
}

function boxes(group) {
	return group.getByRole('button').evaluateAll((buttons) =>
		buttons.map((button) => {
			const { x, y, width, height } = button.getBoundingClientRect();
			return { name: button.textContent.trim(), x: x + scrollX, y: y + scrollY, width, height };
		})
	);
}

function stable(actual, expected) {
	assert.equal(actual.length, expected.length);
	actual.forEach((box, index) => {
		assert.equal(box.name, expected[index].name);
		for (const key of ['x', 'y', 'width', 'height']) {
			assert.ok(Math.abs(box[key] - expected[index][key]) < 1, `${box.name} ${key} moved`);
		}
	});
}

async function editorVisible(page, name) {
	const editors = {
		Text: page.getByLabel('Message', { exact: true }),
		File: page.getByLabel('Add files to upload', { exact: true }),
		Login: page.getByLabel('Username', { exact: true }),
		Card: page.getByLabel('Card number', { exact: true }),
		Identity: page.getByLabel('Full name', { exact: true }),
		Custom: page.getByRole('button', { name: 'Add field', exact: true })
	};
	await editors[name].waitFor({ state: 'visible' });
	assert.equal(await page.getByLabel('Message', { exact: true }).count(), Number(name === 'Text'));
}

async function indicatorAligned(page) {
	await page.waitForFunction(
		() => {
			const group = document.querySelector('[aria-label="Secret type"]');
			const indicator = group?.querySelector('.selection-indicator');
			const selected = group?.querySelector('button[aria-pressed="true"]');
			if (!indicator || !selected || getComputedStyle(indicator).visibility !== 'visible') {
				return false;
			}
			const actual = indicator.getBoundingClientRect();
			const expected = selected.getBoundingClientRect();
			return ['x', 'y', 'width', 'height'].every(
				(key) => Math.abs(actual[key] - expected[key]) < 1
			);
		},
		undefined,
		{ timeout: 3000 }
	);
}

for (const reducedMotion of ['no-preference', 'reduce']) {
	test(`selection background follows rapid choices and viewport wrapping with motion ${reducedMotion}`, async () => {
		const page = await browser.newPage({ viewport: { width: 1280, height: 1000 }, reducedMotion });
		try {
			await fixture(page);
			await openForm(page, forms[0]);
			const group = page.getByRole('group', { name: 'Secret type', exact: true });
			const indicator = group.locator('.selection-indicator');
			await indicatorAligned(page);
			assert.equal(
				await indicator.evaluate((node) => getComputedStyle(node).transitionDuration),
				'0s'
			);
			for (const width of [375, 544, 1280]) {
				await page.setViewportSize({ width, height: 1000 });
				await indicatorAligned(page);
				const initial = await boxes(group);
				// No wait for the background between clicks: the final choice must win.
				for (const name of ['Custom', 'File', 'Identity']) {
					await group.getByRole('button', { name, exact: true }).click();
					stable(await boxes(group), initial);
				}
				assert.equal(
					await group
						.getByRole('button', { name: 'Identity', exact: true })
						.getAttribute('aria-pressed'),
					'true'
				);
				await editorVisible(page, 'Identity');
				await indicatorAligned(page);
				stable(await boxes(group), initial);
				const durations = await indicator.evaluate((node) =>
					getComputedStyle(node).transitionDuration.split(',').map(Number.parseFloat)
				);
				assert.equal(
					durations.some((duration) => duration > 0),
					reducedMotion !== 'reduce'
				);
				if (reducedMotion === 'reduce') {
					assert.equal(
						await group.getByRole('button').evaluateAll((buttons) =>
							buttons.every((button) =>
								getComputedStyle(button)
									.transitionDuration.split(',')
									.every((duration) => Number.parseFloat(duration) === 0)
							)
						),
						true
					);
				}
			}
		} finally {
			await page.close();
		}
	});
}

test('type buttons stay disabled until hydration attaches their handlers', async () => {
	const page = await browser.newPage();
	let releaseScripts;
	let scriptRequested;
	const held = new Promise((resolve) => {
		releaseScripts = resolve;
	});
	const requested = new Promise((resolve) => {
		scriptRequested = resolve;
	});
	try {
		await fixture(page);
		await page.route('**/*', async (route) => {
			if (route.request().resourceType() !== 'script') {
				return route.fallback();
			}
			scriptRequested();
			await held;
			return route.continue();
		});
		await page.goto(baseUrl, { waitUntil: 'commit' });
		const group = page.getByRole('group', { name: 'Secret type', exact: true });
		await group.waitFor();
		await requested;
		assert.equal(await page.locator('#svelte-announcer').count(), 0);
		assert.deepEqual(
			await group
				.getByRole('button')
				.evaluateAll((buttons) => buttons.map((button) => button.disabled)),
			typeNames.map(() => true)
		);
		releaseScripts();
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		assert.deepEqual(
			await group
				.getByRole('button')
				.evaluateAll((buttons) => buttons.map((button) => button.disabled)),
			typeNames.map(() => false)
		);
		await group.getByRole('button', { name: 'File', exact: true }).click();
		await editorVisible(page, 'File');
	} finally {
		releaseScripts();
		await page.unrouteAll({ behavior: 'wait' });
		await page.close();
	}
});

for (const width of [375, 1280]) {
	test(`${width}px type targets stay visible and stationary on hover, focus, and selection`, async () => {
		const page = await browser.newPage({
			viewport: { width, height: 1000 },
			isMobile: width === 375,
			hasTouch: width === 375
		});
		try {
			await fixture(page);
			await openForm(page, forms[0]);
			const group = page.getByRole('group', { name: 'Secret type', exact: true });
			const initial = await boxes(group);
			assert.deepEqual(
				initial.map((box) => box.name),
				typeNames
			);
			const file = await group.getByRole('button', { name: 'File', exact: true }).boundingBox();
			const point = { x: file.x + file.width / 2, y: file.y + file.height / 2 };
			await page.mouse.move(point.x, point.y);
			for (const delay of [50, 400]) {
				await page.waitForTimeout(delay);
				assert.equal(
					await page.evaluate(
						({ x, y }) => document.elementFromPoint(x, y)?.closest('button')?.textContent.trim(),
						point
					),
					'File'
				);
				stable(await boxes(group), initial);
			}
			const pointerAction = width === 375 ? 'tap' : 'click';
			for (const input of ['pointer', 'keyboard']) {
				for (const name of typeNames) {
					const button = group.getByRole('button', { name, exact: true });
					assert.equal(
						await button
							.locator('span')
							.evaluate(
								(label) =>
									label.getBoundingClientRect().width > 0 && getComputedStyle(label).opacity === '1'
							),
						true
					);
					if (input === 'keyboard') {
						await button.focus();
						await page.waitForTimeout(400);
						stable(await boxes(group), initial);
						await page.keyboard.press('Enter');
					} else {
						await button[pointerAction]();
					}
					assert.equal(await button.getAttribute('aria-pressed'), 'true');
					await editorVisible(page, name);
					stable(await boxes(group), initial);
					assert.equal(
						await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
						true
					);
				}
			}
		} finally {
			await page.close();
		}
	});

	test(`${width}px create forms share frame and lifetime geometry without overflow`, async () => {
		const page = await browser.newPage({ viewport: { width, height: width === 375 ? 812 : 1000 } });
		try {
			await fixture(page);
			const frames = [];
			for (const form of forms) {
				await openForm(page, form);
				frames.push(
					await page.evaluate(() => {
						const heading = document.querySelector('h1');
						const header = document.querySelector('header');
						const box = (element) => {
							const { x, y, width, height } = element.getBoundingClientRect();
							return { x, y, width, height };
						};
						const font = getComputedStyle(heading);
						return {
							header: box(header),
							heading: box(heading),
							font: [font.fontFamily, font.fontSize, font.lineHeight]
						};
					})
				);
				if (screenshotDir) {
					await page.screenshot({
						path: join(screenshotDir, `${form.name}-${width}.png`),
						fullPage: true
					});
				}
				const lifetime = page.getByRole('group', { name: form.group, exact: true });
				const initial = await boxes(lifetime);
				for (const [name] of presets) {
					await lifetime.getByRole('button', { name, exact: true }).click();
					stable(await boxes(lifetime), initial);
				}
				await valueInput(page).focus();
				stable(await boxes(lifetime), initial);
				assert.equal(
					await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
					true
				);
			}
			assert.deepEqual(frames[0].header, frames[1].header);
			assert.deepEqual(frames[0].heading, frames[1].heading);
			assert.deepEqual(frames[0].font, frames[1].font);
		} finally {
			await page.close();
		}
	});
}
