import {
	CREDENTIAL_PREFIX,
	CREDENTIAL_VERSION,
	isCredentialType,
	type CredentialEnvelope,
	type CredentialField
} from './schema';

const ENVELOPE_KEYS = new Set(['v', 'type', 'title', 'notes', 'fields']);
const FIELD_KEYS = new Set(['id', 'label', 'value', 'secret']);

export function serializeCredential(envelope: CredentialEnvelope): string {
	const normalized = parseEnvelope(envelope);

	if (normalized === null) {
		throw new Error('Invalid credential envelope');
	}

	return `${CREDENTIAL_PREFIX}${JSON.stringify(normalized)}`;
}

export function parseCredential(value: string): CredentialEnvelope | null {
	if (!value.startsWith(CREDENTIAL_PREFIX)) {
		return null;
	}

	let parsed: unknown;
	try {
		parsed = JSON.parse(value.slice(CREDENTIAL_PREFIX.length));
	} catch {
		return null;
	}

	return parseEnvelope(parsed);
}

function parseEnvelope(value: unknown): CredentialEnvelope | null {
	if (!isRecord(value) || !hasOnlyKeys(value, ENVELOPE_KEYS)) {
		return null;
	}

	if (value.v !== CREDENTIAL_VERSION || !isCredentialType(value.type) || !Array.isArray(value.fields)) {
		return null;
	}

	if (value.title !== undefined && typeof value.title !== 'string') {
		return null;
	}

	if (value.notes !== undefined && typeof value.notes !== 'string') {
		return null;
	}

	const fields: CredentialField[] = [];
	for (const valueField of value.fields) {
		const field = parseField(valueField);
		if (field === null) {
			return null;
		}
		fields.push(field);
	}

	const envelope: CredentialEnvelope = {
		v: CREDENTIAL_VERSION,
		type: value.type,
		fields
	};

	if (value.title !== undefined) {
		envelope.title = value.title;
	}

	if (value.notes !== undefined) {
		envelope.notes = value.notes;
	}

	return envelope;
}

function parseField(value: unknown): CredentialField | null {
	if (!isRecord(value) || !hasOnlyKeys(value, FIELD_KEYS)) {
		return null;
	}

	if (
		typeof value.id !== 'string' ||
		typeof value.label !== 'string' ||
		typeof value.value !== 'string' ||
		typeof value.secret !== 'boolean'
	) {
		return null;
	}

	return {
		id: value.id,
		label: value.label,
		value: value.value,
		secret: value.secret
	};
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: Set<string>): boolean {
	return Object.keys(value).every((key) => allowed.has(key));
}
