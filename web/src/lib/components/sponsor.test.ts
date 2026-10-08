import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import SponsorSlot from './SponsorSlot.svelte';
import { SPONSOR, validateSponsor } from './sponsor';

const fixture = {
	name: 'Synthetic sponsor',
	description: 'A test placement, not an advertiser agreement.',
	url: 'https://sponsor.example.test/'
};

describe('static sponsor boundary', () => {
	it('ships disabled without a blank placement', () => {
		expect(SPONSOR).toBeNull();
		expect(render(SponsorSlot).body).not.toContain('<aside');
	});

	it.each([
		undefined,
		null,
		{},
		{ ...fixture, name: ' ' },
		{ ...fixture, description: '' },
		...[
			'http://example.test/',
			'//example.test/',
			'javascript:alert(1)',
			'https://user:password@example.test/',
			'https://example.test/?id=synthetic',
			'https://example.test/#token',
			'https://example.test/?',
			'https://example.test/#',
			' https://example.test/',
			'https://example.test'
		].map((url) => ({ ...fixture, url }))
	])('invalid configuration disables the placement: %j', (config) => {
		expect(validateSponsor(config)).toBeNull();
	});

	it('renders escaped local text with a literal destination and no referrer', () => {
		const config = { ...fixture, name: '<img src="https://untrusted.test/">' };
		const html = render(SponsorSlot, { props: { config } }).body;
		expect(html).toContain('aria-label="Sponsored"');
		expect(html).toContain('rel="sponsored noopener noreferrer"');
		expect(html).toContain('referrerpolicy="no-referrer"');
		expect(html).toContain(`href="${fixture.url}"`);
		expect(html).toContain('&lt;img');
		expect(html).not.toContain('<img');
	});
});
