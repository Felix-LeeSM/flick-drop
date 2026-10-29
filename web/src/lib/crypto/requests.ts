// Request links on age: docs/architecture/request-links.md. No I/O or key
// persistence. The requester's identity decrypts; submitters hold only its
// recipient. Everything is a standard age file (contracts/age-format-v1.md).
import { decryptWith, encryptToRecipient, generateSecretIdentity, recipientOf } from './age';
import { base64ToBytes, bytesToBase64 } from './encoding';
import { decodeKeyFragment } from './fragment';

const ENVELOPE_VERSION = 2;
// The hybrid recipient is about 1,960 characters; the API caps it at 2,400.
const RECIPIENT_PATTERN = /^age1pq1[02-9ac-hj-np-z]+$/;
const RECIPIENT_MAX_CHARS = 2400;
// The API's cap on the filename age file.
const FILENAME_MAX_BYTES = 4096;
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', {
	fatal: true,
	// biome-ignore lint/style/useNamingConvention: TextDecoder uses the standard ignoreBOM option.
	ignoreBOM: true
});

export type RequestKind = 'text' | 'file';
// Plaintext byte limits, as /api/config advertises them.
export type RequestLimits = { maxTextBytes: number; maxFileBytes: number };
export type RequestEnvelope = { version: 2; encrypted_filename?: string };
// An encrypted submission. size_bytes is the exact age file length the API
// checks; plaintextBytes is what the limits are expressed in.
export type SealedRequest = {
	kind: RequestKind;
	size_bytes: number;
	plaintextBytes: number;
	envelope: RequestEnvelope;
	body: Blob;
};
export type RequestContent = { kind: 'text'; text: string } | { kind: 'file'; file: File };
export type OpenedRequestContent =
	| { kind: 'text'; text: string }
	| { kind: 'file'; blob: Blob; filename: string };
export type OpenedRequestBody = {
	kind: RequestKind;
	encryptedFilename?: Uint8Array;
	body: ReadableStream<Uint8Array>;
};

function invalid(): never {
	throw new Error('Invalid request encryption data.');
}

function base64url(bytes: Uint8Array): string {
	return bytesToBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// SHA-256 over the exact recipient string, unpadded base64url, as the API stores it.
export async function requestFingerprint(recipient: string): Promise<string> {
	return base64url(
		new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(recipient)))
	);
}

export async function generateRequestKeys(): Promise<{
	identity: string;
	public_key: string;
	fingerprint: string;
}> {
	const identity = await generateSecretIdentity();
	const recipient = await recipientOf(identity);
	return { identity, public_key: recipient, fingerprint: await requestFingerprint(recipient) };
}

// The submitter pins the server's recipient to the fingerprint in its link.
export async function importRequestPublicKey(
	value: unknown,
	expectedFingerprint: unknown
): Promise<string> {
	if (
		typeof value !== 'string' ||
		value.length > RECIPIENT_MAX_CHARS ||
		!RECIPIENT_PATTERN.test(value) ||
		typeof expectedFingerprint !== 'string' ||
		(await requestFingerprint(value)) !== expectedFingerprint
	) {
		invalid();
	}
	return value;
}

// The requester checks that the link's identity belongs to this request before
// a consuming open: its recipient must have the stored fingerprint.
export async function importRequestIdentity(
	value: unknown,
	expectedFingerprint: unknown
): Promise<string> {
	const identity = typeof value === 'string' ? decodeKeyFragment(`key=${value}`) : null;
	if (!identity) {
		invalid();
	}
	let recipient: string;
	try {
		recipient = await recipientOf(identity);
	} catch {
		invalid();
	}
	if ((await requestFingerprint(recipient)) !== expectedFingerprint) {
		invalid();
	}
	return identity;
}

function assertLimit(size: number, kind: RequestKind, limits: RequestLimits): void {
	const max = kind === 'text' ? limits.maxTextBytes : limits.maxFileBytes;
	if (!Number.isSafeInteger(max) || max < 0 || size > max) {
		invalid();
	}
}

// Encrypts to the request's recipient. A file is encrypted as it is read,
// 64 KiB at a time, into a Blob the browser may keep on disk.
export async function encryptRequestPayload(
	recipient: string,
	content: RequestContent,
	limits: RequestLimits
): Promise<SealedRequest> {
	if (content.kind === 'text') {
		const bytes = encoder.encode(content.text);
		if (decoder.decode(bytes) !== content.text) {
			invalid();
		}
		assertLimit(bytes.length, 'text', limits);
		const body = await encryptToRecipient(recipient, bytes);
		return {
			kind: 'text',
			size_bytes: body.length,
			plaintextBytes: bytes.length,
			envelope: { version: ENVELOPE_VERSION },
			body: new Blob([body as Uint8Array<ArrayBuffer>])
		};
	}
	if (content.kind !== 'file') {
		invalid();
	}
	const { file } = content;
	const filename = encoder.encode(file.name);
	if (filename.length < 1 || filename.length > 1024 || decoder.decode(filename) !== file.name) {
		invalid();
	}
	assertLimit(file.size, 'file', limits);
	const name = await encryptToRecipient(recipient, filename);
	if (name.length > FILENAME_MAX_BYTES) {
		invalid();
	}
	const body = await new Response(await encryptToRecipient(recipient, file.stream())).blob();
	return {
		kind: 'file',
		size_bytes: body.size,
		plaintextBytes: file.size,
		envelope: { version: ENVELOPE_VERSION, encrypted_filename: bytesToBase64(name) },
		body
	};
}

// Resolves only after the final chunk has been authenticated, so a truncated
// or altered body rejects instead of yielding partial content. A file Blob is
// always application/octet-stream, so it cannot render in the Flick origin.
export async function decryptRequestPayload(
	identity: string,
	opened: OpenedRequestBody,
	limits: RequestLimits
): Promise<OpenedRequestContent> {
	if (opened.kind === 'text') {
		if (opened.encryptedFilename) {
			invalid();
		}
		const plaintext = new Uint8Array(
			await new Response(await decryptWith(identity, opened.body)).arrayBuffer()
		);
		assertLimit(plaintext.length, 'text', limits);
		return { kind: 'text', text: decoder.decode(plaintext) };
	}
	if (opened.kind !== 'file' || !opened.encryptedFilename) {
		invalid();
	}
	const decrypted = await new Response(await decryptWith(identity, opened.body)).blob();
	assertLimit(decrypted.size, 'file', limits);
	const filename = decoder.decode(await decryptWith(identity, opened.encryptedFilename));
	return {
		kind: 'file',
		blob: decrypted.slice(0, decrypted.size, 'application/octet-stream'),
		filename
	};
}

// Decodes the Flick-Encrypted-Filename header value.
export function encryptedFilenameFromHeader(value: string): Uint8Array {
	try {
		const bytes = base64ToBytes(value);
		if (bytes.length > 0 && bytes.length <= FILENAME_MAX_BYTES && bytesToBase64(bytes) === value) {
			return bytes;
		}
	} catch {
		// Falls through to the same rejection as a wrong length.
	}
	invalid();
}
