// biome-ignore-all lint/style/useNamingConvention: field keys are the stored credential field ids (full_name).
// Display text for credential templates and the credential form and view.
// Field labels here are for display only: the stored payload (FLCR1: JSON)
// keeps the English label from web/src/lib/credentials/templates.ts.
export const credentials = {
	templates: {
		login: {
			name: 'Login',
			description: 'Username, password, URL, and one-time code seed.',
			titlePlaceholder: 'Production database',
			fields: {
				username: { label: 'Username', placeholder: 'alice@example.com' },
				password: { label: 'Password' },
				url: { label: 'URL', placeholder: 'https://example.com' },
				totp: { label: 'TOTP seed' }
			}
		},
		card: {
			name: 'Card',
			description: 'Payment card details for one-time delivery.',
			titlePlaceholder: 'Company card',
			fields: {
				cardholder: { label: 'Cardholder', placeholder: 'Alice Lee' },
				number: { label: 'Card number' },
				expires: { label: 'Expires', placeholder: 'MM/YY' },
				cvc: { label: 'CVC' }
			}
		},
		identity: {
			name: 'Identity',
			description: 'Personal identity fields with sensitive identifiers masked.',
			titlePlaceholder: 'Identity package',
			fields: {
				full_name: { label: 'Full name', placeholder: 'Alice Lee' },
				email: { label: 'Email', placeholder: 'alice@example.com' },
				phone: { label: 'Phone' },
				identifier: { label: 'Identifier' }
			}
		},
		custom: {
			name: 'Custom',
			description: 'User-defined label, value, and secret fields.',
			titlePlaceholder: 'Shared credential',
			fields: {}
		}
	},
	title: 'Title',
	label: 'Label',
	secret: 'Secret',
	notes: 'Notes',
	notesPlaceholder: 'Optional',
	addField: 'Add field',
	defaultFieldLabel: (n: number) => `Field ${n}`,
	removeField: 'Remove field',
	removeNamed: (label: string) => `Remove ${label || 'field'}`,
	showValue: 'Show value',
	hideValue: 'Hide value',
	showNamed: (label: string) => `Show ${label}`,
	hideNamed: (label: string) => `Hide ${label}`,
	copyNamed: (label: string) => `Copy ${label}`
};
