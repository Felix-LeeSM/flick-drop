// Model A access proof: a PBKDF2 value derived from the passphrase that gates
// the one-time open. The server stores only its hash; the proof cannot decrypt
// anything (the identity is wrapped separately with age scrypt, see age.ts).
// internal/clientcrypto/crypto.go:DeriveAccessProof is the Go twin, and both
// check the access_proof block of tests/fixtures/client-crypto-vectors.json.

import { base64ToBytes, bytesToBase64 } from './encoding';

export const KDF_ALGORITHM = 'PBKDF2-SHA-256';
export const KDF_ITERATIONS = 600_000;
// Bounds the work a stored KDF block can force on an opener: ten times the default.
export const KDF_MAX_ITERATIONS = 6_000_000;
export const KEY_LENGTH_BITS = 256;
export const SALT_BYTES = 16;
export const ACCESS_VERIFIER_PURPOSE = 'Flick access verifier v1';

export type KdfParams = {
	algorithm: typeof KDF_ALGORITHM;
	salt: string;
	iterations: number;
	key_length_bits: typeof KEY_LENGTH_BITS;
};

type AccessVerifierOptions = {
	salt?: Uint8Array;
	iterations?: number;
};

export type AccessVerifierPayload = {
	kdf: KdfParams;
	proof: string;
};

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
		!Number.isSafeInteger(kdf.iterations) ||
		kdf.iterations < KDF_ITERATIONS ||
		kdf.iterations > KDF_MAX_ITERATIONS ||
		kdf.key_length_bits !== KEY_LENGTH_BITS ||
		kdf.salt.length === 0
	) {
		throw new Error('Unsupported KDF parameters');
	}
}

async function derivePbkdf2Bits(
	material: Uint8Array,
	salt: Uint8Array,
	iterations: number
): Promise<Uint8Array> {
	if (material.byteLength === 0) {
		throw new Error('Passphrase is required');
	}
	if (
		!Number.isSafeInteger(iterations) ||
		iterations < KDF_ITERATIONS ||
		iterations > KDF_MAX_ITERATIONS
	) {
		throw new Error('PBKDF2 iterations are outside the allowed range');
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
