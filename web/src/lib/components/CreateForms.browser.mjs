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
					json: { payload_inline_max_bytes: 1_048_560, max_file_bytes: 268_435_456 }
				});
			}
			assert.ok(['/api/secrets', '/api/requests'].includes(path), `Unexpected API path: ${path}`);
			assert.equal(request.method(), 'POST');
			const body = request.postDataJSON();
			if (path === '/api/secrets') {
				// age carries its own nonces and the passphrase file its own scrypt salt;
				// the create contract has no top-level nonce or kdf.
				assert.equal(body.nonce, undefined);
				assert.equal(body.kdf, undefined);
				assert.match(
					Buffer.from(body.ciphertext, 'base64').toString('latin1'),
					/^age-encryption\.org\/v1\n/
				);
			}
			// Retain only the lifetime: keys and ciphertext are irrelevant to this regression.
			ttls.push(body.ttl_seconds);
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

// The underline tablist holding the type tabs (prototype round 30, T8).
const typeTablist = (page) => page.getByRole('tablist', { name: 'Secret type', exact: true });
const typeTabs = (page) => typeTablist(page).getByRole('tab');
const typeTab = (page, name) => typeTablist(page).getByRole('tab', { name, exact: true });
// Same shape as boxes(), but keyed on the tab role: the tabs are role="tab", which
// getByRole('button') does not match.
const tabBoxes = (page) =>
	typeTabs(page).evaluateAll((tabs) => {
		const scrollLeft = tabs[0]?.closest('[role="tablist"]').scrollLeft ?? 0;
		return tabs.map((tab) => {
			const { x, y, width, height } = tab.getBoundingClientRect();
			// x adds the row's own scroll position back: focusing an off-screen tab
			// legitimately scrolls the row into view, and that must not read as a move.
			return { name: tab.textContent.trim(), x: x + scrollX + scrollLeft, y: y + scrollY, width, height };
		});
	});

// One row at every width: exactly one active tab in the fixed order, the underline
// sits under the active tab, and the row never widens the page — the tablist
// scrolls horizontally where six labels do not fit.
async function typeRow(page, selected) {
	// A selection animates the underline; measure once the transition settles.
	await page.waitForFunction(
		() =>
			document.querySelector('[role="tablist"][aria-label="Secret type"]').getAnimations({
				subtree: true
			}).length === 0
	);
	const row = await tabBoxes(page);
	assert.deepEqual(
		row.map((box) => box.name),
		typeNames
	);
	assert.ok(
		row.every((box) => Math.abs(box.y - row[0].y) < 1),
		'type tabs wrapped'
	);
	assert.deepEqual(
		await typeTabs(page).evaluateAll((tabs) =>
			tabs
				.map((tab) =>
					tab.getAttribute('aria-selected') === 'true' ? tab.textContent.trim() : null
				)
				.filter(Boolean)
		),
		[selected]
	);
	const indicator = await page.waitForFunction(() => {
		const list = document.querySelector('[role="tablist"][aria-label="Secret type"]');
		const bar = list.querySelector('span[aria-hidden="true"]').getBoundingClientRect();
		const active = [...list.querySelectorAll('[role="tab"]')].find(
			(tab) => tab.getAttribute('aria-selected') === 'true'
		);
		const tab = active.getBoundingClientRect();
		return Math.abs(bar.left - tab.left) < 1 && Math.abs(bar.right - tab.right) < 1;
	});
	assert.ok(indicator, 'underline is not under the active tab');
	assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
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

for (const reducedMotion of ['no-preference', 'reduce']) {
	test(`type tabs settle on the last of rapid choices across viewport widths with motion ${reducedMotion}`, async () => {
		const page = await browser.newPage({ viewport: { width: 1280, height: 1000 }, reducedMotion });
		try {
			await fixture(page);
			await openForm(page, forms[0]);
			await typeRow(page, 'Text');
			for (const width of [320, 375, 544, 1280]) {
				await page.setViewportSize({ width, height: 1000 });
				// No wait between clicks: the final choice must win.
				for (const name of ['Custom', 'File', 'Identity']) {
					await typeTab(page, name).click();
				}
				assert.equal(await typeTab(page, 'Identity').getAttribute('aria-selected'), 'true');
				await editorVisible(page, 'Identity');
				await typeRow(page, 'Identity');
				// Custom is the tightest fit on the narrowest phone; picking it scrolls the row.
				await typeTab(page, 'Custom').click();
				await typeRow(page, 'Custom');
				// Reduced motion swaps the sliding underline for an immediate move.
				const transitions = await typeTabs(page).evaluateAll((tabs) =>
					tabs.map((tab) => getComputedStyle(tab).transitionProperty)
				);
				assert.ok(
					transitions.every((property) =>
						reducedMotion === 'reduce' ? property === 'none' : property.includes('color')
					),
					transitions.join(' | ')
				);
			}
		} finally {
			await page.close();
		}
	});
}

test('type tabs stay disabled until hydration attaches their handlers', async () => {
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
		const group = typeTablist(page);
		await group.waitFor();
		await requested;
		assert.equal(await page.locator('#svelte-announcer').count(), 0);
		assert.deepEqual(
			await typeTabs(page).evaluateAll((tabs) => tabs.map((tab) => tab.disabled)),
			typeNames.map(() => true)
		);
		releaseScripts();
		await page.locator('#svelte-announcer').waitFor({ state: 'attached' });
		assert.deepEqual(
			await typeTabs(page).evaluateAll((tabs) => tabs.map((tab) => tab.disabled)),
			typeNames.map(() => false)
		);
		await typeTab(page, 'File').click();
		await editorVisible(page, 'File');
	} finally {
		releaseScripts();
		await page.unrouteAll({ behavior: 'wait' });
		await page.close();
	}
});

for (const width of [375, 1280]) {
	const phone = width < 768;
	test(`${width}px type tabs stay stationary on hover and focus in one row`, async () => {
		const page = await browser.newPage({
			viewport: { width, height: 1000 },
			isMobile: width === 375,
			hasTouch: width === 375
		});
		try {
			await fixture(page);
			await openForm(page, forms[0]);
			await typeRow(page, 'Text');
			const initial = await tabBoxes(page);
			const file = await typeTab(page, 'File').boundingBox();
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
				stable(await tabBoxes(page), initial);
			}
			const pointerAction = phone ? 'tap' : 'click';
			for (const input of ['pointer', 'keyboard']) {
				for (const name of typeNames) {
					const button = typeTab(page, name);
					if (input === 'keyboard') {
						const current = await tabBoxes(page);
						await button.focus();
						await page.waitForTimeout(400);
						stable(await tabBoxes(page), current);
						await page.keyboard.press('Enter');
					} else {
						await button[pointerAction]();
					}
					assert.equal(await button.getAttribute('aria-selected'), 'true');
					await editorVisible(page, name);
					await typeRow(page, name);
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
						const [badge, wordmark] = header.querySelector('a').children;
						return {
							header: box(header),
							heading: box(heading),
							font: [font.fontFamily, font.fontSize, font.lineHeight],
							badge: box(badge),
							wordmark: getComputedStyle(wordmark).fontSize,
							toggle: box(header.querySelector('button[aria-label$=" theme"]'))
						};
					})
				);
				// The wordmark shrank twice in page-level edits; the shared header pins it.
				// The toggle keeps its 36px tap target at every width.
				const frame = frames.at(-1);
				assert.deepEqual([frame.badge.width, frame.badge.height, frame.wordmark], [32, 32, '27px']);
				assert.equal(frame.toggle.height, 36);
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
				// iOS Safari zooms into focused fields under 16px, so the value and unit fields keep
				// 16px at every width and are scaled down to the preset chips' text size.
				const sizes = await valueInput(page).evaluate((input) => {
					const scale = getComputedStyle(input.parentElement).scale;
					const factor =
						scale === 'none' ? 1 : Number.parseFloat(scale) / (scale.endsWith('%') ? 100 : 1);
					const preset = input.closest('[role="group"]').querySelector('button');
					return {
						field: getComputedStyle(input).fontSize,
						unit: getComputedStyle(input.parentElement.querySelector('select')).fontSize,
						rendered: Number.parseFloat(getComputedStyle(input).fontSize) * factor,
						preset: Number.parseFloat(getComputedStyle(preset).fontSize)
					};
				});
				assert.deepEqual([sizes.field, sizes.unit], ['16px', '16px']);
				assert.equal(sizes.rendered, sizes.preset);
				// Tapping the chip beside the digits still focuses the value field.
				const edge = await valueInput(page).evaluate((input) => {
					input.blur();
					const chip = input.closest('div').getBoundingClientRect();
					const point = { x: chip.left + 3, y: chip.top + chip.height / 2 };
					return { ...point, target: document.elementFromPoint(point.x, point.y).tagName };
				});
				assert.equal(edge.target, 'LABEL');
				await page.mouse.click(edge.x, edge.y);
				assert.equal(
					await valueInput(page).evaluate((input) => input === document.activeElement),
					true
				);
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

for (const width of [320, 375]) {
	test(`shared header wraps within a ${width}px viewport instead of widening the page when text is enlarged`, async () => {
		const page = await browser.newPage({ viewport: { width, height: 700 } });
		try {
			await fixture(page);
			// Both directions: the home page's link label ("Request a secret") is longer than the
			// request page's ("Send a secret"), and the longer one is the one that used to overflow.
			for (const form of forms) {
				await openForm(page, form);
				// Header sizes are in rem, so a larger text setting widens the wordmark and the link.
				await page.evaluate(() => {
					document.documentElement.style.fontSize = '150%';
				});
				// The buttons animate their new size (transition-all); measure once it settles.
				await page.waitForFunction(
					() => document.querySelector('header').getAnimations({ subtree: true }).length === 0
				);
				// Scoped to the header's own elements, not document.documentElement.scrollWidth, so an
				// overflow elsewhere on the page cannot fail this header check. (The type selector used
				// to overflow here at large text sizes; its cells now share the row's width.)
				const header = await page.evaluate(() => {
					const box = (element) => element?.getBoundingClientRect().toJSON();
					const root = document.querySelector('header');
					return {
						brand: box(root.querySelector('a')),
						link: box(root.querySelector('nav a')),
						toggle: box(root.querySelector('button[aria-label$=" theme"]'))
					};
				});
				// Only the request page keeps a text link; the home header is brand + theme only
				// (the request and code entries live in the adaptive heading, prototype R1).
				// Each text-sized action either fits beside the wordmark or wraps below it, and
				// none crosses the viewport edge.
				for (const action of [header.link, header.toggle].filter(Boolean)) {
					assert.ok(action.right <= width, form.path);
					assert.ok(
						action.left >= header.brand.right || action.top >= header.brand.bottom,
						form.path
					);
				}
			}
		} finally {
			await page.close();
		}
	});
}

test('320px shared header stays one row beside the theme toggle without overflow', async () => {
	const page = await browser.newPage({ viewport: { width: 320, height: 700 } });
	try {
		await fixture(page);
		for (const form of forms) {
			await openForm(page, form);
			const header = await page.evaluate(() => {
				const box = (element) => element?.getBoundingClientRect().toJSON();
				const root = document.querySelector('header');
				return {
					brand: box(root.querySelector('a')),
					link: box(root.querySelector('nav a')),
					toggle: box(root.querySelector('button[aria-label$=" theme"]')),
					overflow: document.documentElement.scrollWidth > innerWidth
				};
			});
			assert.equal(header.overflow, false);
			// One row at the narrowest supported width: brand and toggle share the
			// wordmark's vertical center, and the request page's text link sits between them.
			const first = header.link ?? header.toggle;
			assert.ok(header.brand.right < first.left, form.path);
			assert.equal(
				first.top + first.height / 2,
				header.brand.top + header.brand.height / 2,
				form.path
			);
			if (header.link) {
				assert.ok(header.link.right <= header.toggle.left);
				assert.equal(header.link.top, header.toggle.top);
			}
		}
	} finally {
		await page.close();
	}
});

for (const theme of ['light', 'dark']) {
	test(`disabled Create link reads as disabled in the ${theme} theme`, async () => {
		const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
		try {
			await page.addInitScript((value) => localStorage.setItem('flick-theme', value), theme);
			await fixture(page);
			await openForm(page, forms[0]);
			const button = page.locator('form button[type="submit"]');
			assert.equal(await button.isDisabled(), true);
			assert.equal(
				await page.evaluate(() => document.documentElement.classList.contains('dark')),
				theme === 'dark'
			);
			// The solid foreground at half opacity — visibly off in both themes — and
			// disabled:shadow-none drops the cta glow: disabled:shadow-none resets the
			// shadow variables, so every remaining box-shadow layer reads as transparent.
			const style = await button.evaluate((element) => {
				const probe = document.createElement('span');
				probe.style.backgroundColor = 'var(--foreground)';
				document.body.append(probe);
				const foreground = getComputedStyle(probe).backgroundColor;
				probe.remove();
				const computed = getComputedStyle(element);
				const layers =
					computed.boxShadow === 'none' ? [] : computed.boxShadow.split(/,(?![^(]*\))/);
				return {
					foreground,
					background: computed.backgroundColor,
					opacity: computed.opacity,
					shadow: layers.some((layer) => !layer.includes('rgba(0, 0, 0, 0)'))
				};
			});
			assert.deepEqual(style, {
				foreground: style.foreground,
				background: style.foreground,
				opacity: '0.5',
				shadow: false
			});
		} finally {
			await page.close();
		}
	});
}

test('the passphrase switch reveals its field with in-field generate and reveal actions', async () => {
	const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
	try {
		await fixture(page);
		await openForm(page, forms[0]);
		const field = page.getByLabel('Additional passphrase', { exact: true });
		assert.equal(await field.count(), 0);
		const toggle = page.getByRole('switch', { name: 'Protect with an additional passphrase' });
		await toggle.click();
		await field.waitFor({ state: 'visible' });
		// The placeholder names the field; the switch row above is its context.
		assert.equal(await field.getAttribute('placeholder'), 'Add a passphrase');
		// The field starts at the row's left edge — the switch row has no indent.
		const rowLabel = await page.locator('label[for="use-passphrase"]').boundingBox();
		const box = await field.boundingBox();
		assert.ok(Math.abs(box.x - rowLabel.x) < 1);
		// The in-field actions sit inside the field's right edge: reveal last,
		// generate immediately to its left.
		const reveal = await page.getByRole('button', { name: 'Show passphrase' }).boundingBox();
		assert.equal(reveal.x + reveal.width, box.x + box.width);
		const generate = await page.getByRole('button', { name: 'Generate passphrase' }).boundingBox();
		assert.equal(generate.x + generate.width, reveal.x);
	} finally {
		await page.close();
	}
});

test('Generate fills a revealed passphrase that never reaches the API', async () => {
	const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
	try {
		const mock = await fixture(page);
		await openForm(page, forms[0]);
		await page.getByRole('switch', { name: 'Protect with an additional passphrase' }).click();
		const field = page.getByLabel('Additional passphrase', { exact: true });
		const generate = page.getByRole('button', { name: 'Generate passphrase', exact: true });
		await generate.click();
		const first = await field.inputValue();
		// Four groups of four Crockford base32 symbols: 80 random bits.
		assert.match(first, /^[0-9a-hjkmnp-tv-z]{4}(-[0-9a-hjkmnp-tv-z]{4}){3}$/);
		// The sender has to copy it, so it shows instead of staying masked.
		assert.equal(await field.getAttribute('type'), 'text');
		await generate.click();
		const generated = await field.inputValue();
		assert.notEqual(generated, first);
		await page.getByLabel('Message', { exact: true }).fill('Synthetic generated passphrase');
		const response = page.waitForResponse(
			(reply) =>
				reply.request().method() === 'POST' && new URL(reply.url()).pathname === '/api/secrets'
		);
		await submitButton(page).click();
		// Model A sends an access proof and a scrypt-wrapped identity, never the passphrase.
		const body = (await response).request().postData();
		assert.equal(body.includes(generated), false);
		assert.equal(body.includes(generated.replaceAll('-', '')), false);
		assert.equal(mock.ttls.length, 1);
	} finally {
		await page.close();
	}
});

test('protection rows explain themselves through the info popovers', async () => {
	const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
	try {
		await fixture(page);
		await openForm(page, forms[0]);
		const popover = page.locator('#passphrase-privacy');
		assert.equal(await popover.evaluate((node) => getComputedStyle(node).visibility), 'hidden');
		await page.getByRole('button', { name: "Why the server can't see this secret" }).focus();
		await page.waitForFunction(
			() => getComputedStyle(document.querySelector('#passphrase-privacy')).visibility === 'visible'
		);

		// Switching to the code tab swaps in the confirmation procedure.
		await page
			.getByRole('group', { name: 'Delivery method' })
			.getByRole('button', { name: 'Send with code' })
			.click();
		const confirmPopover = page.locator('#confirmation-procedure');
		assert.equal(
			await confirmPopover.evaluate((node) => getComputedStyle(node).visibility),
			'hidden'
		);
		// Hovering the info button shows it; the popover lives outside the label, so
		// clicking its text cannot toggle the switch.
		await page.getByRole('button', { name: 'How confirmation works' }).hover();
		await confirmPopover.getByText('Check that they match.').click();
		assert.equal(
			await page
				.getByRole('switch', { name: 'Require confirmation before transfer' })
				.getAttribute('data-state'),
			'unchecked'
		);
	} finally {
		await page.close();
	}
});

test('delivery tabs swap the heading and slide their underline', async () => {
	const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
	try {
		await fixture(page);
		await openForm(page, forms[0]);
		const heading = page.locator('h1');
		assert.equal((await heading.textContent()).trim(), 'Create a secret');
		const tabs = page.getByRole('group', { name: 'Delivery method' });
		const linkTab = tabs.getByRole('button', { name: 'Create link' });
		const codeTab = tabs.getByRole('button', { name: 'Send with code' });
		assert.equal(await linkTab.getAttribute('aria-pressed'), 'true');
		await codeTab.click();
		assert.equal(await codeTab.getAttribute('aria-pressed'), 'true');
		assert.equal(await linkTab.getAttribute('aria-pressed'), 'false');
		assert.equal((await heading.textContent()).trim(), 'Send a secret');
		// The underline indicator slides under the active tab (each tab owns half the row).
		await page.waitForFunction(
			() =>
				document.querySelector('[aria-label="Delivery method"]').getAnimations({
					subtree: true
				}).length === 0
		);
		const shift = await page.evaluate(() => {
			const group = document.querySelector('[aria-label="Delivery method"]');
			const bar = group.querySelector('span[aria-hidden="true"]').getBoundingClientRect();
			const box = group.getBoundingClientRect();
			return { left: bar.left - box.left, width: bar.width, groupWidth: box.width };
		});
		assert.ok(Math.abs(shift.width - shift.groupWidth / 2) < 1, 'underline is not half the row');
		assert.ok(
			Math.abs(shift.left - shift.groupWidth / 2) < 1,
			'underline did not slide to the code tab'
		);
	} finally {
		await page.close();
	}
});
