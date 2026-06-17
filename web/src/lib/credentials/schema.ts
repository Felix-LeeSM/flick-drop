export const CREDENTIAL_PREFIX = 'BLCR1:';
export const CREDENTIAL_VERSION = 1;

export const CREDENTIAL_TYPES = ['login', 'card', 'identity', 'custom'] as const;

export type CredentialType = (typeof CREDENTIAL_TYPES)[number];

export type CredentialField = {
	id: string;
	label: string;
	value: string;
	secret: boolean;
};

export type CredentialEnvelope = {
	v: typeof CREDENTIAL_VERSION;
	type: CredentialType;
	title?: string;
	notes?: string;
	fields: CredentialField[];
};

export function isCredentialType(value: unknown): value is CredentialType {
	const credentialTypes: readonly string[] = CREDENTIAL_TYPES;
	return typeof value === 'string' && credentialTypes.includes(value);
}
