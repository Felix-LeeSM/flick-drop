// Pins how Flick uses age (c2sp.org/age) — see contracts/age-format-v1.md.
// Everything here is a standard age recipient, identity, or file; Flick adds no
// construction of its own. internal/clientcrypto/age.go is the Go twin, and
// tests/fixtures/age/ holds files each side produced for the other to open.

import {
	Decrypter,
	Encrypter,
	generateHybridIdentity,
	identityToRecipient,
	type ReadableStreamWithSize
} from 'age-encryption';

// The only identity type a secret uses: hybrid ML-KEM-768 + X25519. Body and
// filename are age files encrypted to its recipient.
export const IDENTITY_PREFIX = 'AGE-SECRET-KEY-PQ-1';
// age's own default scrypt work factor (log2 N) for the Model A passphrase file
// that wraps the identity. age-encryption refuses to decrypt above 20, which is
// also the Go client's cap.
export const WRAP_WORK_FACTOR = 18;
// An identity string is under 100 bytes; a larger unwrapped file is not one.
const MAX_IDENTITY_BYTES = 1024;

export function generateSecretIdentity(): Promise<string> {
	return generateHybridIdentity();
}

// Accepts an identity from a Model B fragment or an unwrapped Model A file and
// refuses any other identity type.
export function parseSecretIdentity(value: string): string {
	const identity = value.trim();
	if (!identity.startsWith(IDENTITY_PREFIX)) {
		throw new Error(`identity must start with ${IDENTITY_PREFIX}`);
	}
	return identity;
}

// A stream input returns a stream: each 64 KiB chunk is encrypted as it is read.
// Its size() gives the exact ciphertext length for a plaintext length.
export async function encryptTo(identity: string, input: Uint8Array | string): Promise<Uint8Array>;
export async function encryptTo(
	identity: string,
	input: ReadableStream<Uint8Array>
): Promise<ReadableStreamWithSize>;
export async function encryptTo(
	identity: string,
	input: Uint8Array | string | ReadableStream<Uint8Array>
): Promise<Uint8Array | ReadableStreamWithSize> {
	const encrypter = new Encrypter();
	encrypter.addRecipient(await identityToRecipient(parseSecretIdentity(identity)));
	// Overloads do not narrow through a union argument.
	return encrypter.encrypt(input as Uint8Array);
}

// Each chunk is authenticated before it is returned; a truncated stream errors
// at the end instead of yielding a short plaintext.
export async function decryptWith(identity: string, input: Uint8Array): Promise<Uint8Array>;
export async function decryptWith(
	identity: string,
	input: ReadableStream<Uint8Array>
): Promise<ReadableStream<Uint8Array>>;
export async function decryptWith(
	identity: string,
	input: Uint8Array | ReadableStream<Uint8Array>
): Promise<Uint8Array | ReadableStream<Uint8Array>> {
	const decrypter = new Decrypter();
	decrypter.addIdentity(parseSecretIdentity(identity));
	return await decrypter.decrypt(input as Uint8Array);
}

// Model A: the identity as a standard age passphrase file. The server stores it
// and returns it only from a verified open.
export async function wrapIdentity(identity: string, passphrase: string): Promise<Uint8Array> {
	if (passphrase === '') {
		throw new Error('passphrase is required');
	}
	const encrypter = new Encrypter();
	encrypter.setScryptWorkFactor(WRAP_WORK_FACTOR);
	encrypter.setPassphrase(passphrase);
	return await encrypter.encrypt(parseSecretIdentity(identity));
}

export async function unwrapIdentity(wrapped: Uint8Array, passphrase: string): Promise<string> {
	if (passphrase === '') {
		throw new Error('passphrase is required');
	}
	const decrypter = new Decrypter();
	decrypter.addPassphrase(passphrase);
	const plaintext = await decrypter.decrypt(wrapped);
	if (plaintext.length > MAX_IDENTITY_BYTES) {
		throw new Error('wrapped identity is too large');
	}
	return parseSecretIdentity(new TextDecoder('utf-8', { fatal: true }).decode(plaintext));
}
