import { CREDENTIAL_TYPES, type CredentialType } from './templates';

export const CREDENTIAL_PREFIX = 'BLCR1:';
export const CREDENTIAL_VERSION = 1;

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
