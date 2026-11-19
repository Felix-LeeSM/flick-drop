import { describe, expect, it } from 'vitest';

import { en } from '#lib/i18n/messages/en/index.js';
import { ko } from '#lib/i18n/messages/ko/index.js';
import { buildEnvelope } from './build';
import { displayFieldLabel } from './templates';

describe('displayFieldLabel', () => {
	it('서식 필드는 저장된 영어 라벨 대신 보는 사람의 언어로 보여 준다', () => {
		expect.assertions(2);

		// Given: 영어 라벨 'Password'로 저장된 로그인 정보
		const field = { id: 'password', label: 'Password' };

		// When: 한국어와 영어 카탈로그로 표시 라벨을 고르면
		const korean = displayFieldLabel('login', field, ko.credentials);
		const english = displayFieldLabel('login', field, en.credentials);

		// Then: 각 언어의 서식 라벨이 나온다
		expect(korean).toBe('비밀번호');
		expect(english).toBe('Password');
	});

	it('서식에 없는 필드 id는 저장된 라벨을 그대로 보여 준다', () => {
		expect.assertions(2);

		// Given: 사용자 지정 필드와 서식에 없는 id를 가진 옛 필드
		const custom = { id: 'custom-1', label: 'API key' };
		const unknown = { id: 'pin', label: 'PIN' };

		// When / Then: 한국어 카탈로그로 골라도 저장된 라벨이 나온다
		expect(displayFieldLabel('custom', custom, ko.credentials)).toBe('API key');
		expect(displayFieldLabel('login', unknown, ko.credentials)).toBe('PIN');
	});

	it('저장 형식의 라벨은 표시 언어와 상관없이 영어로 남는다', () => {
		expect.assertions(1);

		// Given / When: 로그인 서식으로 새 봉투를 만들면
		const envelope = buildEnvelope('login');

		// Then: 저장될 라벨은 영어 원문이다
		expect(envelope.fields.map((field) => field.label)).toEqual([
			'Username',
			'Password',
			'URL',
			'TOTP seed'
		]);
	});
});
