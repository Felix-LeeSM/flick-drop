import { describe, expect, it } from 'vitest';

import {
	addCustomField,
	buildEnvelope,
	CREDENTIAL_TEMPLATES,
	CREDENTIAL_TYPES,
	getCredentialTemplate,
	parseCredential,
	removeField,
	serializeCredential,
	setFieldValue
} from './index';

describe('credential templates', () => {
	it('defines one template for every supported credential type', () => {
		expect.assertions(2);

		expect(CREDENTIAL_TYPES).toEqual(['login', 'card', 'identity', 'custom']);
		expect(CREDENTIAL_TEMPLATES.map((template) => template.type)).toEqual(CREDENTIAL_TYPES);
	});

	it('keeps template field ids unique within each type', () => {
		expect.assertions(CREDENTIAL_TEMPLATES.length);

		for (const template of CREDENTIAL_TEMPLATES) {
			const ids = template.fields.map((field) => field.id);

			expect(new Set(ids).size).toBe(ids.length);
		}
	});
});

describe('credential envelope builders', () => {
	it('builds an empty envelope from a template definition', () => {
		expect.assertions(1);

		expect(buildEnvelope('login')).toEqual({
			v: 1,
			type: 'login',
			title: '',
			notes: '',
			fields: [
				{
					id: 'username',
					label: 'Username',
					value: '',
					secret: false
				},
				{
					id: 'password',
					label: 'Password',
					value: '',
					secret: true
				},
				{
					id: 'url',
					label: 'URL',
					value: '',
					secret: false
				},
				{
					id: 'totp',
					label: 'TOTP seed',
					value: '',
					secret: true
				}
			]
		});
	});

	it('applies initial title, notes, and field values', () => {
		expect.assertions(1);

		expect(
			buildEnvelope('card', {
				title: 'Operations card',
				notes: 'Use for renewals only.',
				values: {
					cardholder: 'Alice Lee',
					expires: '12/30'
				}
			})
		).toMatchObject({
			type: 'card',
			title: 'Operations card',
			notes: 'Use for renewals only.',
			fields: [
				{
					id: 'cardholder',
					value: 'Alice Lee'
				},
				{
					id: 'number',
					value: ''
				},
				{
					id: 'expires',
					value: '12/30'
				},
				{
					id: 'cvc',
					value: ''
				}
			]
		});
	});

	it('builds envelopes accepted by the serializer', () => {
		expect.assertions(1);

		const envelope = buildEnvelope('identity', {
			title: 'Passport handoff',
			values: {
				full_name: 'Alice Lee',
				identifier: 'A12345678'
			}
		});

		expect(parseCredential(serializeCredential(envelope))).toEqual(envelope);
	});

	it('sets a field value immutably', () => {
		expect.assertions(4);

		const envelope = buildEnvelope('login');
		const updated = setFieldValue(envelope, 'password', 'secret value');

		expect(updated).not.toBe(envelope);
		expect(updated.fields).not.toBe(envelope.fields);
		expect(envelope.fields.find((field) => field.id === 'password')?.value).toBe('');
		expect(updated.fields.find((field) => field.id === 'password')?.value).toBe('secret value');
	});

	it('adds custom fields immutably with generated ids', () => {
		expect.assertions(5);

		const envelope = buildEnvelope('custom');
		const first = addCustomField(envelope, { value: 'visible' });
		const second = addCustomField(first, {
			label: 'API key',
			value: 'key-value',
			secret: true
		});

		expect(first).not.toBe(envelope);
		expect(envelope.fields).toEqual([]);
		expect(first.fields[0]).toEqual({ id: 'custom-1', label: 'Field 1', value: 'visible', secret: false });
		expect(second.fields[1]).toEqual({
			id: 'custom-2',
			label: 'API key',
			value: 'key-value',
			secret: true
		});
		expect(getCredentialTemplate('custom').allowCustomFields).toBe(true);
	});

	it('rejects custom field additions for fixed templates', () => {
		expect.assertions(1);

		expect(() => addCustomField(buildEnvelope('login'))).toThrow(
			'Login credentials do not support custom fields'
		);
	});

	it('rejects duplicate custom field ids', () => {
		expect.assertions(1);

		const envelope = addCustomField(buildEnvelope('custom'), { id: 'api-key' });

		expect(() => addCustomField(envelope, { id: 'api-key' })).toThrow(
			'Custom field id already exists: api-key'
		);
	});

	it('removes a field immutably', () => {
		expect.assertions(4);

		const envelope = addCustomField(addCustomField(buildEnvelope('custom')), {
			label: 'Second'
		});
		const updated = removeField(envelope, 'custom-1');

		expect(updated).not.toBe(envelope);
		expect(updated.fields).not.toBe(envelope.fields);
		expect(envelope.fields).toHaveLength(2);
		expect(updated.fields).toEqual([{ id: 'custom-2', label: 'Second', value: '', secret: false }]);
	});
});
