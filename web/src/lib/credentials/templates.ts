type TemplateSpec = {
	label: string;
	icon: string;
	description: string;
	titlePlaceholder: string;
	allowCustomFields: boolean;
	fields: readonly TemplateFieldDef[];
};

export type CredentialInputKind = 'text' | 'email' | 'url' | 'tel' | 'password' | 'textarea';

export type TemplateFieldDef = {
	id: string;
	label: string;
	secret: boolean;
	input: CredentialInputKind;
	placeholder?: string;
	autocomplete?: string;
};

export const CREDENTIAL_TEMPLATE_SPECS = {
	login: {
		label: 'Login',
		icon: 'key-round',
		description: 'Username, password, URL, and one-time code seed.',
		titlePlaceholder: 'Production database',
		allowCustomFields: false,
		fields: [
			{
				id: 'username',
				label: 'Username',
				secret: false,
				input: 'text',
				placeholder: 'alice@example.com',
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
				placeholder: 'https://example.com',
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
		description: 'Payment card details for one-time delivery.',
		titlePlaceholder: 'Company card',
		allowCustomFields: false,
		fields: [
			{
				id: 'cardholder',
				label: 'Cardholder',
				secret: false,
				input: 'text',
				placeholder: 'Alice Lee',
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
				placeholder: 'MM/YY',
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
		description: 'Personal identity fields with sensitive identifiers masked.',
		titlePlaceholder: 'Identity package',
		allowCustomFields: false,
		fields: [
			{
				id: 'full_name',
				label: 'Full name',
				secret: false,
				input: 'text',
				placeholder: 'Alice Lee',
				autocomplete: 'name'
			},
			{
				id: 'email',
				label: 'Email',
				secret: false,
				input: 'email',
				placeholder: 'alice@example.com',
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
		description: 'User-defined label, value, and secret fields.',
		titlePlaceholder: 'Shared credential',
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
