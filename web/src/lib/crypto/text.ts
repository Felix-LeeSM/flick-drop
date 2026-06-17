export const KDF_ALGORITHM = 'PBKDF2-SHA-256';
export const KDF_ITERATIONS = 600_000;
export const KEY_LENGTH_BITS = 256;
export const SALT_BYTES = 16;
export const NONCE_BYTES = 12;

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

type EncryptOptions = {
	salt?: Uint8Array;
	nonce?: Uint8Array;
	iterations?: number;
};

export async function encryptText(
	plaintext: string,
	passphrase: string,
	options: EncryptOptions = {}
): Promise<EncryptedTextPayload> {
	const plaintextBytes = new TextEncoder().encode(plaintext);
	const salt = options.salt ?? randomBytes(SALT_BYTES);
	const nonce = options.nonce ?? randomBytes(NONCE_BYTES);
	const iterations = options.iterations ?? KDF_ITERATIONS;
	const key = await deriveAesGcmKey(passphrase, salt, iterations);
	const ciphertext = await crypto.subtle.encrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(nonce) },
		key,
		arrayBufferFrom(plaintextBytes)
	);

	return {
		ciphertext: bytesToBase64(new Uint8Array(ciphertext)),
		nonce: bytesToBase64(nonce),
		kdf: {
			algorithm: KDF_ALGORITHM,
			salt: bytesToBase64(salt),
			iterations,
			key_length_bits: KEY_LENGTH_BITS
		},
		size_bytes: plaintextBytes.byteLength
	};
}

export async function decryptText(payload: EncryptedTextPayload, passphrase: string): Promise<string> {
	assertKdf(payload.kdf);

	const salt = base64ToBytes(payload.kdf.salt);
	const nonce = base64ToBytes(payload.nonce);
	const ciphertext = base64ToBytes(payload.ciphertext);
	const key = await deriveAesGcmKey(passphrase, salt, payload.kdf.iterations);
	const plaintext = await crypto.subtle.decrypt(
		{ name: 'AES-GCM', iv: arrayBufferFrom(nonce) },
		key,
		arrayBufferFrom(ciphertext)
	);

	return new TextDecoder().decode(plaintext);
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

	const passphraseKey = await crypto.subtle.importKey(
		'raw',
		arrayBufferFrom(new TextEncoder().encode(passphrase)),
		'PBKDF2',
		false,
		['deriveKey']
	);

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
	const chunkSize = 0x8000;
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
