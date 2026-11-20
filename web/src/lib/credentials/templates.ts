import { type Messages, t } from '#lib/i18n/index.js';

// `label` is the stored English name (FLCR1 payload); screen text comes from i18n `credentials`.
type TemplateSpec = {
	label: string;
	icon: string;
	allowCustomFields: boolean;
	fields: readonly TemplateFieldDef[];
};

export type CredentialInputKind = 'text' | 'email' | 'url' | 'tel' | 'password' | 'textarea';

export type TemplateFieldDef = {
	id: string;
	label: string;
	secret: boolean;
	input: CredentialInputKind;
	autocomplete?: string;
};

export const CREDENTIAL_TEMPLATE_SPECS = {
	login: {
		label: 'Login',
		icon: 'key-round',
		allowCustomFields: false,
		fields: [
			{
				id: 'username',
				label: 'Username',
				secret: false,
				input: 'text',
				autocomplete: 'username'
			},
			{
				id: 'password',
				label: 'Password',
				secret: true,
				input: 'password',
				autocomplete: 'new-password'
			},
			{
				id: 'url',
				label: 'URL',
				secret: false,
				input: 'url',
				autocomplete: 'url'
			},
			{
				id: 'totp',
				label: 'TOTP seed',
				secret: true,
				input: 'password',
				autocomplete: 'off'
			}
		]
	},
	card: {
		label: 'Card',
		icon: 'credit-card',
		allowCustomFields: false,
		fields: [
			{
				id: 'cardholder',
				label: 'Cardholder',
				secret: false,
				input: 'text',
				autocomplete: 'cc-name'
			},
			{
				id: 'number',
				label: 'Card number',
				secret: true,
				input: 'text',
				autocomplete: 'cc-number'
			},
			{
				id: 'expires',
				label: 'Expires',
				secret: false,
				input: 'text',
				autocomplete: 'cc-exp'
			},
			{
				id: 'cvc',
				label: 'CVC',
				secret: true,
				input: 'password',
				autocomplete: 'cc-csc'
			}
		]
	},
	identity: {
		label: 'Identity',
		icon: 'id-card',
		allowCustomFields: false,
		fields: [
			{
				id: 'full_name',
				label: 'Full name',
				secret: false,
				input: 'text',
				autocomplete: 'name'
			},
			{
				id: 'email',
				label: 'Email',
				secret: false,
				input: 'email',
				autocomplete: 'email'
			},
			{
				id: 'phone',
				label: 'Phone',
				secret: false,
				input: 'tel',
				autocomplete: 'tel'
			},
			{
				id: 'identifier',
				label: 'Identifier',
				secret: true,
				input: 'password',
				autocomplete: 'off'
			}
		]
	},
	custom: {
		label: 'Custom',
		icon: 'list-plus',
		allowCustomFields: true,
		fields: []
	}
} as const satisfies Record<string, TemplateSpec>;

export type CredentialType = keyof typeof CREDENTIAL_TEMPLATE_SPECS;

export type TemplateDef = TemplateSpec & {
	type: CredentialType;
};

export const CREDENTIAL_TYPES = Object.keys(CREDENTIAL_TEMPLATE_SPECS) as CredentialType[];

export const CREDENTIAL_TEMPLATES = CREDENTIAL_TYPES.map(getCredentialTemplate);

export function getCredentialTemplate(type: CredentialType): TemplateDef {
	return {
		type,
		...CREDENTIAL_TEMPLATE_SPECS[type]
	};
}

type CredentialsText = Messages['credentials'];
type TemplateText = CredentialsText['templates'][CredentialType];
type FieldText = { label: string; placeholder?: string };

export function credentialTemplateText(
	type: CredentialType,
	text: CredentialsText = t().credentials
): TemplateText {
	return text.templates[type];
}

export function credentialFieldText(
	type: CredentialType,
	fieldId: string,
	text: CredentialsText = t().credentials
): FieldText | undefined {
	const fields: Partial<Record<string, FieldText>> = text.templates[type].fields;
	return Object.hasOwn(fields, fieldId) ? fields[fieldId] : undefined;
}

// Only a field still carrying the template's English label is translated; others show as stored.
export function displayFieldLabel(
	type: CredentialType,
	field: { id: string; label: string },
	text: CredentialsText = t().credentials
): string {
	const templateField = getCredentialTemplate(type).fields.find(({ id }) => id === field.id);
	if (templateField?.label !== field.label) {
		return field.label;
	}
	return credentialFieldText(type, field.id, text)?.label ?? field.label;
}
