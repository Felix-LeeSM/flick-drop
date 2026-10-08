// Request v1 only: docs/architecture/request-links.md. No I/O or key persistence.
import { base64ToBytes, bytesToBase64 } from './text';

const RSA = { name: 'RSA-OAEP', hash: 'SHA-256' } as const;
const ALGORITHM = 'RSA-OAEP-256+A256GCM';
const ENVELOPE_MAX_BYTES = 4096;
const KEY_MAX_CHARS = 4096;
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', {
	fatal: true,
	// biome-ignore lint/style/useNamingConvention: TextDecoder uses the standard ignoreBOM option.
	ignoreBOM: true
});

export type RequestKind = 'text' | 'file';
// Plaintext byte limits; callers subtract the GCM tag from the inline ciphertext limit.
export type RequestLimits = { maxTextBytes: number; maxFileBytes: number };
export type RequestEnvelope = {
	version: 1;
	algorithm: typeof ALGORITHM;
	wrapped_key: string;
	nonce: string;
	encrypted_filename?: { nonce: string; ciphertext: string };
};
export type RequestPayload = {
	kind: RequestKind;
	size_bytes: number;
	envelope: RequestEnvelope;
	ciphertext: string;
};
export type RequestContent =
	| { kind: 'text'; text: string }
	| { kind: 'file'; bytes: Uint8Array<ArrayBuffer>; filename: string };

function subtle(): SubtleCrypto {
	if (!globalThis.crypto?.subtle) {
		throw new Error('Request encryption requires Web Crypto in a secure context.');
	}
	return globalThis.crypto.subtle;
}

function invalid(): never {
	throw new Error('Invalid request encryption data.');
}

function base64(value: unknown, min: number, max = min): Uint8Array<ArrayBuffer> {
	if (typeof value !== 'string' || value.length > 4 * Math.ceil(max / 3)) {
		invalid();
	}
	const bytes = base64ToBytes(value);
	if (bytes.length < min || bytes.length > max || bytesToBase64(bytes) !== value) {
		invalid();
	}
	return bytes as Uint8Array<ArrayBuffer>;
}

function base64url(bytes: Uint8Array): string {
	return bytesToBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function decodeUrl(value: unknown, maxChars: number): Uint8Array<ArrayBuffer> {
	if (
		typeof value !== 'string' ||
		!value.length ||
		value.length > maxChars ||
		!/^[A-Za-z0-9_-]+$/.test(value)
	) {
		invalid();
	}
	const standard = value.replace(/-/g, '+').replace(/_/g, '/');
	const bytes = base64ToBytes(standard + '='.repeat((4 - (standard.length % 4)) % 4));
	if (base64url(bytes) !== value) {
		invalid();
	}
	return bytes as Uint8Array<ArrayBuffer>;
}

function assertKey(key: CryptoKey, type: 'public' | 'private'): void {
	const algorithm = key.algorithm as RsaHashedKeyAlgorithm;
	if (
		key.type !== type ||
		algorithm.name !== RSA.name ||
		algorithm.hash?.name !== RSA.hash ||
		algorithm.modulusLength !== 2048 ||
		bytesToBase64(algorithm.publicExponent) !== 'AQAB'
	) {
		invalid();
	}
}

async function fingerprint(spki: ArrayBuffer): Promise<string> {
	return base64url(new Uint8Array(await subtle().digest('SHA-256', spki)));
}

async function assertFingerprint(spki: ArrayBuffer, expected: unknown): Promise<void> {
	if (decodeUrl(expected, 43).length !== 32 || (await fingerprint(spki)) !== expected) {
		invalid();
	}
}

export async function generateRequestKeys(): Promise<CryptoKeyPair> {
	return await subtle().generateKey(
		{ ...RSA, modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]) },
		true,
		['encrypt', 'decrypt']
	);
}

export async function exportRequestPublicKey(
	key: CryptoKey
): Promise<{ public_key: string; fingerprint: string }> {
	assertKey(key, 'public');
	const spki = await subtle().exportKey('spki', key);
	return { public_key: bytesToBase64(new Uint8Array(spki)), fingerprint: await fingerprint(spki) };
}

export async function exportRequestPrivateKey(key: CryptoKey): Promise<string> {
	assertKey(key, 'private');
	return base64url(new Uint8Array(await subtle().exportKey('pkcs8', key)));
}

export async function importRequestPublicKey(
	value: unknown,
	expectedFingerprint: unknown
): Promise<CryptoKey> {
	const bytes = base64(value, 1, 3072);
	const key = await subtle().importKey('spki', bytes, RSA, true, ['encrypt']);
	assertKey(key, 'public');
	const canonical = await subtle().exportKey('spki', key);
	if (bytesToBase64(new Uint8Array(canonical)) !== value) {
		invalid();
	}
	await assertFingerprint(canonical, expectedFingerprint);
	return key;
}

export async function importRequestPrivateKey(
	value: unknown,
	expectedFingerprint: unknown
): Promise<CryptoKey> {
	const bytes = decodeUrl(value, KEY_MAX_CHARS);
	const key = await subtle().importKey('pkcs8', bytes, RSA, true, ['decrypt']);
	assertKey(key, 'private');
	if ((await exportRequestPrivateKey(key)) !== value) {
		invalid();
	}
	// Export only n/e into a fresh public JWK; never pass private JWK fields on.
	const { n, e } = await subtle().exportKey('jwk', key);
	const publicKey = await subtle().importKey('jwk', { kty: 'RSA', n, e }, RSA, true, ['encrypt']);
	await assertFingerprint(await subtle().exportKey('spki', publicKey), expectedFingerprint);
	// Native DER import may accept inconsistent RSA private components. Prove
	// the private half works before a caller consumes the one-time response.
	const challenge = crypto.getRandomValues(new Uint8Array(32));
	const sealed = await subtle().encrypt(RSA, publicKey, challenge);
	const opened = new Uint8Array(await subtle().decrypt(RSA, key, sealed));
	if (bytesToBase64(opened) !== bytesToBase64(challenge)) {
		invalid();
	}
	return key;
}

function object(value: unknown, fields: string[]): Record<string, unknown> {
	if (!value || typeof value !== 'object' || Array.isArray(value)) {
		invalid();
	}
	const record = value as Record<string, unknown>;
	const keys = Object.keys(record);
	if (keys.length !== fields.length || keys.some((key) => !fields.includes(key))) {
		invalid();
	}
	return record;
}

function kind(value: unknown): RequestKind {
	if (value !== 'text' && value !== 'file') {
		invalid();
	}
	return value;
}

export function parseRequestEnvelope(value: unknown, payloadKind: RequestKind): RequestEnvelope {
	kind(payloadKind);
	let parsed = value;
	if (typeof value === 'string') {
		if (value.length > ENVELOPE_MAX_BYTES || encoder.encode(value).length > ENVELOPE_MAX_BYTES) {
			invalid();
		}
		parsed = JSON.parse(value);
	}
	const fields = ['version', 'algorithm', 'wrapped_key', 'nonce'];
	if (payloadKind === 'file') {
		fields.push('encrypted_filename');
	}
	const record = object(parsed, fields);
	if (record.version !== 1 || record.algorithm !== ALGORITHM) {
		invalid();
	}
	base64(record.wrapped_key, 256);
	base64(record.nonce, 12);
	const envelope: RequestEnvelope = {
		version: 1,
		algorithm: ALGORITHM,
		wrapped_key: record.wrapped_key as string,
		nonce: record.nonce as string
	};
	if (payloadKind === 'file') {
		const filename = object(record.encrypted_filename, ['nonce', 'ciphertext']);
		base64(filename.nonce, 12);
		base64(filename.ciphertext, 17, 1040);
		if (filename.nonce === record.nonce) {
			invalid();
		}
		envelope.encrypted_filename = {
			nonce: filename.nonce as string,
			ciphertext: filename.ciphertext as string
		};
	}
	if (encoder.encode(JSON.stringify(envelope)).length > ENVELOPE_MAX_BYTES) {
		invalid();
	}
	return envelope;
}

export function serializeRequestEnvelope(value: unknown, payloadKind: RequestKind): string {
	return JSON.stringify(parseRequestEnvelope(value, payloadKind));
}

function binding(
	requestId: string,
	payloadKind: RequestKind,
	purpose: 'key' | 'payload' | 'filename'
): Uint8Array<ArrayBuffer> {
	// API-issued identifiers are bounded; reject LF/CR and lossy UTF-8 input.
	if (
		typeof requestId !== 'string' ||
		!requestId.length ||
		requestId.length > 256 ||
		/[\r\n]/.test(requestId) ||
		decoder.decode(encoder.encode(requestId)) !== requestId
	) {
		invalid();
	}
	kind(payloadKind);
	return encoder.encode(`Flick request v1\n${requestId}\n${payloadKind}\n${purpose}`);
}

function assertSize(value: unknown, payloadKind: RequestKind, limits: RequestLimits): number {
	for (const limit of [limits.maxTextBytes, limits.maxFileBytes]) {
		if (!Number.isSafeInteger(limit) || limit < 0 || limit > Number.MAX_SAFE_INTEGER - 16) {
			invalid();
		}
	}
	const max = payloadKind === 'text' ? limits.maxTextBytes : limits.maxFileBytes;
	if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0 || value > max) {
		invalid();
	}
	return value;
}

export async function encryptRequestPayload(
	requestId: string,
	publicKey: CryptoKey,
	content: RequestContent,
	limits: RequestLimits
): Promise<RequestPayload> {
	const payloadKind = kind(content.kind);
	const label = binding(requestId, payloadKind, 'key');
	assertKey(publicKey, 'public');
	const bytes = content.kind === 'text' ? encoder.encode(content.text) : content.bytes;
	if (content.kind === 'text' && decoder.decode(bytes) !== content.text) {
		invalid();
	}
	const size = assertSize(bytes.byteLength, payloadKind, limits);
	const filename = content.kind === 'file' ? encoder.encode(content.filename) : undefined;
	if (
		filename &&
		(filename.length < 1 ||
			filename.length > 1024 ||
			(content.kind === 'file' && decoder.decode(filename) !== content.filename))
	) {
		invalid();
	}
	subtle();
	const rawKey = crypto.getRandomValues(new Uint8Array(32));
	const nonce = crypto.getRandomValues(new Uint8Array(12));
	const filenameNonce = filename ? crypto.getRandomValues(new Uint8Array(12)) : undefined;
	if (filenameNonce && bytesToBase64(filenameNonce) === bytesToBase64(nonce)) {
		invalid();
	}
	const key = await subtle().importKey('raw', rawKey, 'AES-GCM', false, ['encrypt']);
	const wrapped = await subtle().encrypt({ name: RSA.name, label }, publicKey, rawKey);
	const envelope: RequestEnvelope = {
		version: 1,
		algorithm: ALGORITHM,
		wrapped_key: bytesToBase64(new Uint8Array(wrapped)),
		nonce: bytesToBase64(nonce)
	};
	const ciphertext = await subtle().encrypt(
		{
			name: 'AES-GCM',
			iv: nonce,
			additionalData: binding(requestId, payloadKind, 'payload'),
			tagLength: 128
		},
		key,
		bytes
	);
	if (filename && filenameNonce) {
		const encrypted = await subtle().encrypt(
			{
				name: 'AES-GCM',
				iv: filenameNonce,
				additionalData: binding(requestId, payloadKind, 'filename'),
				tagLength: 128
			},
			key,
			filename
		);
		envelope.encrypted_filename = {
			nonce: bytesToBase64(filenameNonce),
			ciphertext: bytesToBase64(new Uint8Array(encrypted))
		};
	}
	return {
		kind: payloadKind,
		size_bytes: size,
		envelope,
		ciphertext: bytesToBase64(new Uint8Array(ciphertext))
	};
}

export async function decryptRequestPayload(
	requestId: string,
	privateKey: CryptoKey,
	value: unknown,
	limits: RequestLimits
): Promise<RequestContent> {
	const record = object(value, ['kind', 'size_bytes', 'envelope', 'ciphertext']);
	const payloadKind = kind(record.kind);
	const label = binding(requestId, payloadKind, 'key');
	const size = assertSize(record.size_bytes, payloadKind, limits);
	const envelope = parseRequestEnvelope(record.envelope, payloadKind);
	const ciphertext = base64(record.ciphertext, size + 16);
	assertKey(privateKey, 'private');
	const rawKey = await subtle().decrypt(
		{ name: RSA.name, label },
		privateKey,
		base64(envelope.wrapped_key, 256)
	);
	if (rawKey.byteLength !== 32) {
		invalid();
	}
	const key = await subtle().importKey('raw', rawKey, 'AES-GCM', false, ['decrypt']);
	const bytes = new Uint8Array(
		await subtle().decrypt(
			{
				name: 'AES-GCM',
				iv: base64(envelope.nonce, 12),
				additionalData: binding(requestId, payloadKind, 'payload'),
				tagLength: 128
			},
			key,
			ciphertext
		)
	);
	if (bytes.length !== size) {
		invalid();
	}
	if (payloadKind === 'text') {
		return { kind: 'text', text: decoder.decode(bytes) };
	}
	const filename = envelope.encrypted_filename;
	if (!filename) {
		invalid();
	}
	const name = await subtle().decrypt(
		{
			name: 'AES-GCM',
			iv: base64(filename.nonce, 12),
			additionalData: binding(requestId, payloadKind, 'filename'),
			tagLength: 128
		},
		key,
		base64(filename.ciphertext, 17, 1040)
	);
	return { kind: 'file', bytes, filename: decoder.decode(name) };
}
