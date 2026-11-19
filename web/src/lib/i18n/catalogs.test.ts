import { describe, expect, it } from 'vitest';
import { LOCALES } from './locale.svelte';
import { en } from './messages/en';
import { ja } from './messages/ja';
import { ko } from './messages/ko';

const catalogs = { en, ko, ja };

function shape(value: unknown, path = ''): Record<string, string> {
	if (typeof value === 'string') {
		return { [path]: value.trim() === '' ? 'empty string' : 'string' };
	}
	if (typeof value === 'function') {
		return { [path]: `function(${value.length})` };
	}
	if (Array.isArray(value)) {
		return Object.assign(
			{ [path]: `array(${value.length})` },
			...value.map((item, i) => shape(item, `${path}[${i}]`))
		);
	}
	if (value && typeof value === 'object') {
		return Object.assign(
			{},
			...Object.entries(value).map(([key, item]) => shape(item, path ? `${path}.${key}` : key))
		);
	}
	return { [path]: typeof value };
}

describe('i18n catalogs', () => {
	it('지원하는 언어마다 카탈로그가 있다', () => {
		expect(Object.keys(catalogs).sort()).toEqual([...LOCALES].sort());
	});

	for (const locale of LOCALES.filter((code) => code !== 'en')) {
		it(`${locale}에 en의 키가 빠짐없이 같은 모양으로 있다`, () => {
			const source = shape(en);

			const target = shape(catalogs[locale]);

			expect(target).toEqual(source);
		});
	}
});
