// Standard age only (contracts/age-format-v1.md). Go twin: internal/clientcrypto/age.go;
// tests/fixtures/age/ holds files each side produced for the other.

import {
	Decrypter,
	Encrypter,
	generateHybridIdentity,
	identityToRecipient,
	type ReadableStreamWithSize
} from 'age-encryption';

// The only identity type: hybrid ML-KEM-768 + X25519.
export const IDENTITY_PREFIX = 'AGE-SECRET-KEY-PQ-1';
// age's default scrypt work factor; age-encryption refuses above 20, also the Go client's cap.
export const WRAP_WORK_FACTOR = 18;
// An identity string is under 100 bytes; a larger unwrapped file is not one.
const MAX_IDENTITY_BYTES = 1024;

export function generateSecretIdentity(): Promise<string> {
	return generateHybridIdentity();
}

export function parseSecretIdentity(value: string): string {
	const identity = value.trim();
	if (!identity.startsWith(IDENTITY_PREFIX)) {
		throw new Error(`identity must start with ${IDENTITY_PREFIX}`);
	}
	return identity;
}

// size() gives the exact ciphertext length for a plaintext length.
export async function encryptTo(identity: string, input: Uint8Array | string): Promise<Uint8Array>;
export async function encryptTo(
	identity: string,
	input: ReadableStream<Uint8Array>
): Promise<ReadableStreamWithSize>;
export async function encryptTo(
	identity: string,
	input: Uint8Array | string | ReadableStream<Uint8Array>
): Promise<Uint8Array | ReadableStreamWithSize> {
	return encryptToRecipient(await recipientOf(identity), input as Uint8Array);
}

export function recipientOf(identity: string): Promise<string> {
	return identityToRecipient(parseSecretIdentity(identity));
}

// A request submitter holds only the requester's recipient, never the identity.
export function encryptToRecipient(
	recipient: string,
	input: Uint8Array | string
): Promise<Uint8Array>;
export function encryptToRecipient(
	recipient: string,
	input: ReadableStream<Uint8Array>
): Promise<ReadableStreamWithSize>;
export function encryptToRecipient(
	recipient: string,
	input: Uint8Array | string | ReadableStream<Uint8Array>
): Promise<Uint8Array | ReadableStreamWithSize> {
	const encrypter = new Encrypter();
	encrypter.addRecipient(recipient);
	// Overloads do not narrow through a union argument.
	return encrypter.encrypt(input as Uint8Array);
}

// Each chunk authenticates before return; a truncated stream errors instead of yielding short plaintext.
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

// The server stores it and returns it only from a verified open.
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
