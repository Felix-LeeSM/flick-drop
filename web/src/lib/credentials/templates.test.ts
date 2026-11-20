import { describe, expect, it } from 'vitest';

import { en } from '#lib/i18n/messages/en/index.js';
import { ko } from '#lib/i18n/messages/ko/index.js';
import { buildEnvelope } from './build';
import { displayFieldLabel } from './templates';

describe('displayFieldLabel', () => {
	it('서식 필드는 저장된 영어 라벨 대신 보는 사람의 언어로 보여 준다', () => {
		expect.assertions(2);

		const field = { id: 'password', label: 'Password' };

		const korean = displayFieldLabel('login', field, ko.credentials);
		const english = displayFieldLabel('login', field, en.credentials);

		expect(korean).toBe('비밀번호');
		expect(english).toBe('Password');
	});

	it('서식에 없는 필드 id는 저장된 라벨을 그대로 보여 준다', () => {
		expect.assertions(2);

		const custom = { id: 'custom-1', label: 'API key' };
		const unknown = { id: 'pin', label: 'PIN' };

		expect(displayFieldLabel('custom', custom, ko.credentials)).toBe('API key');
		expect(displayFieldLabel('login', unknown, ko.credentials)).toBe('PIN');
	});

	it('서식 필드라도 다른 라벨로 저장됐으면 저장된 라벨을 보여 준다', () => {
		expect.assertions(1);

		const field = { id: 'password', label: 'Bank PIN' };

		expect(displayFieldLabel('login', field, ko.credentials)).toBe('Bank PIN');
	});

	it('저장 형식의 라벨은 표시 언어와 상관없이 영어로 남는다', () => {
		expect.assertions(1);

		const envelope = buildEnvelope('login');

		expect(envelope.fields.map((field) => field.label)).toEqual([
			'Username',
			'Password',
			'URL',
			'TOTP seed'
		]);
	});
});
