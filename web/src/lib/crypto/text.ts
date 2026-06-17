export const KDF_ALGORITHM = 'PBKDF2-SHA-256';
export const KDF_ITERATIONS = 600_000;
export const KEY_LENGTH_BITS = 256;
export const SALT_BYTES = 16;
export const NONCE_BYTES = 12;
export const ACCESS_VERIFIER_PURPOSE = 'BurnLink access verifier v1';

export type KdfParams = {
	algorithm: typeof KDF_ALGORITHM;
	salt: string;
	iterations: number;
	key_length_bits: typeof KEY_LENGTH_BITS;
};

export type EncryptedTextPayload = {
	ciphertext: string;
	nonce: string;
	kdf: KdfParams;
	size_bytes: number;
};

export type EncryptedFilePayload = EncryptedTextPayload & {
	encrypted_filename: string;
	content_type: string;
};

type EncryptOptions = {
	salt?: Uint8Array;
	nonce?: Uint8Array;
	iterations?: number;
};

type EncryptFileOptions = EncryptOptions & {
	filenameNonce?: Uint8Array;
};

type EncryptionContext = {
	key: CryptoKey;
	kdf: KdfParams;
};

type EncryptedMetadata = {
	nonce: string;
	ciphertext: string;
};

type AccessVerifierOptions = {
	salt?: Uint8Array;
	iterations?: number;
};

export type AccessVerifierPayload = {
	kdf: KdfParams;
	proof: string;
};

export function encryptText(
	plaintext: string,
	passphrase: string,
	options: EncryptOptions = {}
): Promise<EncryptedTextPayload> {
	const plaintextBytes = new TextEncoder().encode(plaintext);
	return encryptBytes(plaintextBytes, passphrase, options);
}

export async function decryptText(
	payload: EncryptedTextPayload,
	passphrase: string
): Promise<string> {
	const plaintext = await decryptBytes(payload, passphrase);
	return new TextDecoder().decode(plaintext);
}

export async function encryptFile(
	file: File,
	passphrase: string,
	options: EncryptFileOptions = {}
): Promise<EncryptedFilePayload> {
	const fileBytes = new Uint8Array(await file.arrayBuffer());
	const context = await createEncryptionContext(passphrase, options);
	const encryptedPayload = await encryptBytesWithContext(
		fileBytes,
		context,
		options.nonce ?? randomBytes(NONCE_BYTES)
	);
	const encryptedFilename = await encryptMetadata(
		file.name || 'burnlink-file',
		context.key,
		options.filenameNonce ?? randomBytes(NONCE_BYTES)
	);

	return {
		...encryptedPayload,
		encrypted_filename: JSON.stringify(encryptedFilename),
		content_type: file.type || 'application/octet-stream'
	};
}

export async function decryptFile(
	payload: EncryptedFilePayload,
	passphrase: string
): Promise<{ bytes: Uint8Array; filename: string; contentType: string }> {
	assertKdf(payload.kdf);

	const salt = base64ToBytes(payload.kdf.salt);
	const key = await deriveAesGcmKey(passphrase, salt, payload.kdf.iterations);
	const bytes = await decryptBytesWithKey(payload, key);
	const filename = await decryptMetadata(payload.encrypted_filename, key);

	return {
		bytes,
		filename,
		contentType: payload.content_type || 'application/octet-stream'
	};
}

export async function encryptBytes(
	plaintextBytes: Uint8Array,
	passphrase: string,
	options: EncryptOptions = {}
): Promise<EncryptedTextPayload> {
	const context = await createEncryptionContext(passphrase, options);
	return encryptBytesWithContext(
		plaintextBytes,
		context,
		options.nonce ?? randomBytes(NONCE_BYTES)
	);
}

export async function decryptBytes(
	payload: EncryptedTextPayload,
	passphrase: string
): Promise<Uint8Array> {
	assertKdf(payload.kdf);

	const salt = base64ToBytes(payload.kdf.salt);
	const key = await deriveAesGcmKey(passphrase, salt, payload.kdf.iterations);
	return decryptBytesWithKey(payload, key);
}

async function createEncryptionContext(
	passphrase: string,
	options: EncryptOptions
): Promise<EncryptionContext> {
	const salt = options.salt ?? randomBytes(SALT_BYTES);
	const iterations = options.iterations ?? KDF_ITERATIONS;

	return {
		key: await deriveAesGcmKey(passphrase, salt, iterations),
		kdf: {
			algorithm: KDF_ALGORITHM,
			salt: bytesToBase64(salt),
			iterations,
			key_length_bits: KEY_LENGTH_BITS
		}
	};
}

async function encryptBytesWithContext(
	plaintextBytes: Uint8Array,
	context: EncryptionContext,
	nonce: Uint8Array
): Promise<EncryptedTextPayload> {
	const ciphertext = await crypto.subtle.encrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(nonce) },
		context.key,
		arrayBufferFrom(plaintextBytes)
	);

	return {
		ciphertext: bytesToBase64(new Uint8Array(ciphertext)),
		nonce: bytesToBase64(nonce),
		kdf: context.kdf,
		size_bytes: plaintextBytes.byteLength
	};
}

async function decryptBytesWithKey(
	payload: EncryptedTextPayload,
	key: CryptoKey
): Promise<Uint8Array> {
	const nonce = base64ToBytes(payload.nonce);
	const ciphertext = base64ToBytes(payload.ciphertext);
	const plaintext = await crypto.subtle.decrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(nonce) },
		key,
		arrayBufferFrom(ciphertext)
	);

	return new Uint8Array(plaintext);
}

async function encryptMetadata(
	value: string,
	key: CryptoKey,
	nonce: Uint8Array
): Promise<EncryptedMetadata> {
	const ciphertext = await crypto.subtle.encrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(nonce) },
		key,
		arrayBufferFrom(new TextEncoder().encode(value))
	);

	return {
		nonce: bytesToBase64(nonce),
		ciphertext: bytesToBase64(new Uint8Array(ciphertext))
	};
}

async function decryptMetadata(value: string, key: CryptoKey): Promise<string> {
	if (value.length === 0) {
		throw new Error('Encrypted filename is required');
	}

	const metadata = JSON.parse(value) as EncryptedMetadata;
	const plaintext = await crypto.subtle.decrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(base64ToBytes(metadata.nonce)) },
		key,
		arrayBufferFrom(base64ToBytes(metadata.ciphertext))
	);

	return new TextDecoder().decode(plaintext);
}

export async function createAccessVerifier(
	passphrase: string,
	options: AccessVerifierOptions = {}
): Promise<AccessVerifierPayload> {
	const salt = options.salt ?? randomBytes(SALT_BYTES);
	const iterations = options.iterations ?? KDF_ITERATIONS;

	return {
		kdf: {
			algorithm: KDF_ALGORITHM,
			salt: bytesToBase64(salt),
			iterations,
			key_length_bits: KEY_LENGTH_BITS
		},
		proof: await deriveAccessProof(passphrase, {
			algorithm: KDF_ALGORITHM,
			salt: bytesToBase64(salt),
			iterations,
			key_length_bits: KEY_LENGTH_BITS
		})
	};
}

export async function deriveAccessProof(passphrase: string, kdf: KdfParams): Promise<string> {
	assertKdf(kdf);

	const salt = base64ToBytes(kdf.salt);
	const proof = await derivePbkdf2Bits(accessVerifierMaterial(passphrase), salt, kdf.iterations);
	return bytesToBase64(proof);
}

export function assertKdf(kdf: KdfParams): void {
	if (
		kdf.algorithm !== KDF_ALGORITHM ||
		kdf.iterations < KDF_ITERATIONS ||
		kdf.key_length_bits !== KEY_LENGTH_BITS ||
		kdf.salt.length === 0
	) {
		throw new Error('Unsupported KDF parameters');
	}
}

async function deriveAesGcmKey(
	passphrase: string,
	salt: Uint8Array,
	iterations: number
): Promise<CryptoKey> {
	if (passphrase.length === 0) {
		throw new Error('Passphrase is required');
	}
	if (iterations < KDF_ITERATIONS) {
		throw new Error('PBKDF2 iterations are below the minimum');
	}

	const passphraseKey = await importPbkdf2Material(new TextEncoder().encode(passphrase));

	return crypto.subtle.deriveKey(
		{
			name: 'PBKDF2',
			hash: 'SHA-256',
			salt: arrayBufferFrom(salt),
			iterations
		},
		passphraseKey,
		{ name: 'AES-GCM', length: KEY_LENGTH_BITS },
		false,
		['encrypt', 'decrypt']
	);
}

async function derivePbkdf2Bits(
	material: Uint8Array,
	salt: Uint8Array,
	iterations: number
): Promise<Uint8Array> {
	if (material.byteLength === 0) {
		throw new Error('Passphrase is required');
	}
	if (iterations < KDF_ITERATIONS) {
		throw new Error('PBKDF2 iterations are below the minimum');
	}

	const passphraseKey = await importPbkdf2Material(material);
	const bits = await crypto.subtle.deriveBits(
		{
			name: 'PBKDF2',
			hash: 'SHA-256',
			salt: arrayBufferFrom(salt),
			iterations
		},
		passphraseKey,
		KEY_LENGTH_BITS
	);
	return new Uint8Array(bits);
}

function importPbkdf2Material(material: Uint8Array): Promise<CryptoKey> {
	return crypto.subtle.importKey('raw', arrayBufferFrom(material), 'PBKDF2', false, [
		'deriveBits',
		'deriveKey'
	]);
}

function accessVerifierMaterial(passphrase: string): Uint8Array {
	if (passphrase.length === 0) {
		throw new Error('Passphrase is required');
	}
	return new TextEncoder().encode(`${ACCESS_VERIFIER_PURPOSE}\0${passphrase}`);
}

function arrayBufferFrom(bytes: Uint8Array): ArrayBuffer {
	return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
}

function randomBytes(length: number): Uint8Array {
	const bytes = new Uint8Array(length);
	crypto.getRandomValues(bytes);
	return bytes;
}

export function bytesToBase64(bytes: Uint8Array): string {
	let binary = '';
	const chunkSize = 0x80_00;
	for (let index = 0; index < bytes.length; index += chunkSize) {
		const chunk = bytes.subarray(index, index + chunkSize);
		binary += String.fromCharCode(...chunk);
	}
	return btoa(binary);
}

export function base64ToBytes(value: string): Uint8Array {
	const binary = atob(value);
	const bytes = new Uint8Array(binary.length);
	for (let index = 0; index < binary.length; index += 1) {
		bytes[index] = binary.charCodeAt(index);
	}
	return bytes;
}
