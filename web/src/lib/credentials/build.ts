import {
	CREDENTIAL_VERSION,
	type CredentialEnvelope,
	type CredentialField
} from './schema';
import { getCredentialTemplate, type CredentialType } from './templates';

export type BuildEnvelopeOptions = {
	title?: string;
	notes?: string;
	values?: Readonly<Record<string, string>>;
};

export type CustomFieldOptions = {
	id?: string;
	label?: string;
	value?: string;
	secret?: boolean;
};

export function buildEnvelope(
	type: CredentialType,
	options: BuildEnvelopeOptions = {}
): CredentialEnvelope {
	const template = getCredentialTemplate(type);

	return {
		v: CREDENTIAL_VERSION,
		type,
		title: options.title ?? '',
		notes: options.notes ?? '',
		fields: template.fields.map((field) => ({
			id: field.id,
			label: field.label,
			value: options.values?.[field.id] ?? '',
			secret: field.secret
		}))
	};
}

export function setFieldValue(
	envelope: CredentialEnvelope,
	fieldId: string,
	value: string
): CredentialEnvelope {
	return {
		...envelope,
		fields: envelope.fields.map((field) =>
			field.id === fieldId
				? {
						...field,
						value
					}
				: field
		)
	};
}

export function addCustomField(
	envelope: CredentialEnvelope,
	options: CustomFieldOptions = {}
): CredentialEnvelope {
	const template = getCredentialTemplate(envelope.type);

	if (!template.allowCustomFields) {
		throw new Error(`${template.label} credentials do not support custom fields`);
	}

	const field = buildCustomField(envelope.fields, options);

	return {
		...envelope,
		fields: [...envelope.fields, field]
	};
}

export function removeField(envelope: CredentialEnvelope, fieldId: string): CredentialEnvelope {
	return {
		...envelope,
		fields: envelope.fields.filter((field) => field.id !== fieldId)
	};
}

function buildCustomField(
	existingFields: readonly CredentialField[],
	options: CustomFieldOptions
): CredentialField {
	const nextIndex = nextCustomFieldIndex(existingFields);
	const id = options.id ?? `custom-${nextIndex}`;

	if (id.length === 0) {
		throw new Error('Custom field id is required');
	}

	if (existingFields.some((field) => field.id === id)) {
		throw new Error(`Custom field id already exists: ${id}`);
	}

	return {
		id,
		label: options.label ?? `Field ${nextIndex}`,
		value: options.value ?? '',
		secret: options.secret ?? false
	};
}

function nextCustomFieldIndex(fields: readonly CredentialField[]): number {
	const ids = new Set(fields.map((field) => field.id));
	let index = fields.length + 1;

	while (ids.has(`custom-${index}`)) {
		index += 1;
	}

	return index;
}
