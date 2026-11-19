import { describe, expect, it } from 'vitest';
import { detectLocale } from './locale.svelte';

describe('detectLocale', () => {
	it('저장된 수동 선택이 브라우저 언어보다 먼저다', () => {
		expect(detectLocale('ja', ['ko-KR', 'en-US'])).toBe('ja');
	});

	it('브라우저 언어 목록에서 Flick이 가진 첫 언어를 고른다', () => {
		expect(detectLocale(null, ['fr-FR', 'ko-KR', 'ja'])).toBe('ko');
		expect(detectLocale(null, ['JA-jp'])).toBe('ja');
	});

	it('알 수 없는 저장값과 없는 언어는 영어로 돌아간다', () => {
		expect(detectLocale('de', ['fr-FR', 'zh-CN'])).toBe('en');
		expect(detectLocale(null, [])).toBe('en');
	});
});
