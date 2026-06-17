import { describe, expect, it } from 'vitest';

import {
	CREDENTIAL_PREFIX,
	CREDENTIAL_VERSION,
	type CredentialEnvelope,
	type CredentialType,
	parseCredential,
	serializeCredential
} from './index';

const credentialTypes: CredentialType[] = ['login', 'card', 'identity', 'custom'];

function sampleEnvelope(type: CredentialType): CredentialEnvelope {
	return {
		v: CREDENTIAL_VERSION,
		type,
		title: `${type} title`,
		notes: `${type} notes`,
		fields: [
			{
				id: `${type}-username`,
				label: 'Username',
				value: 'alice@example.com',
				secret: false
			},
			{
				id: `${type}-password`,
				label: 'Password',
				value: 'correct horse battery staple',
				secret: true
			}
		]
	};
}

describe('credential serialization', () => {
	it('round trips each supported credential type', () => {
		expect.assertions(credentialTypes.length * 3);

		for (const type of credentialTypes) {
			const envelope = sampleEnvelope(type);
			const serialized = serializeCredential(envelope);

			expect(serialized.startsWith(CREDENTIAL_PREFIX)).toBe(true);
			expect(serialized).toContain('"v":1');
			expect(parseCredential(serialized)).toEqual(envelope);
		}
	});

	it('preserves empty optional title, notes, and fields', () => {
		expect.assertions(1);

		const envelope: CredentialEnvelope = {
			v: CREDENTIAL_VERSION,
			type: 'custom',
			title: '',
			notes: '',
			fields: []
		};

		expect(parseCredential(serializeCredential(envelope))).toEqual(envelope);
	});

	it('returns null for legacy text and malformed payloads', () => {
		expect.assertions(12);

		const invalidInputs = [
			'legacy free text',
			'',
			JSON.stringify(sampleEnvelope('login')),
			`${CREDENTIAL_PREFIX}{`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), v: undefined })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), type: undefined })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), fields: undefined })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), v: 2 })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), type: 'server' })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), fields: {} })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), fields: [{ id: 'x' }] })}`,
			`${CREDENTIAL_PREFIX}${JSON.stringify({ ...sampleEnvelope('login'), extra: true })}`
		];

		for (const input of invalidInputs) {
			expect(parseCredential(input)).toBeNull();
		}
	});

	it('rejects invalid envelopes during serialization', () => {
		expect.assertions(3);

		expect(() =>
			serializeCredential({
				...sampleEnvelope('login'),
				fields: [{ id: 'password', label: 'Password', value: 'secret', secret: 'yes' }]
			} as unknown as CredentialEnvelope)
		).toThrow('Invalid credential envelope');
		expect(() =>
			serializeCredential({
				...sampleEnvelope('login'),
				type: 'unknown'
			} as unknown as CredentialEnvelope)
		).toThrow('Invalid credential envelope');
		expect(() =>
			serializeCredential({
				...sampleEnvelope('login'),
				notes: null
			} as unknown as CredentialEnvelope)
		).toThrow('Invalid credential envelope');
	});
});
