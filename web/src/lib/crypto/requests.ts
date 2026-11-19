// Request links (docs/architecture/request-links.md): submitters hold only the recipient; no I/O or key persistence.
import { decryptWith, encryptToRecipient, generateSecretIdentity, recipientOf } from './age';
import { base64ToBytes, bytesToBase64 } from './encoding';
import { decodeKeyFragment } from './fragment';

const ENVELOPE_VERSION = 2;
// The hybrid recipient is about 1,960 characters; the API caps it at 2,400.
const RECIPIENT_PATTERN = /^age1pq1[02-9ac-hj-np-z]+$/;
const RECIPIENT_MAX_CHARS = 2400;
// The API's cap on the filename age file.
const FILENAME_MAX_BYTES = 4096;
// The plaintext filename, in UTF-8 bytes, on both sides.
const NAME_MAX_BYTES = 1024;
// Fits the server's 4,096-byte encrypted_note cap alongside the PQ header.
export const REQUEST_NOTE_MAX_BYTES = 1024;
const ENCRYPTED_NOTE_MAX_BYTES = 4096;
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', {
	fatal: true,
	// biome-ignore lint/style/useNamingConvention: TextDecoder uses the standard ignoreBOM option.
	ignoreBOM: true
});

export type RequestKind = 'text' | 'file';
export type RequestLimits = { maxTextBytes: number; maxFileBytes: number };
export type RequestEnvelope = { version: 2; encrypted_filename?: string };
// size_bytes is the exact age file length the API checks.
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

// Before a consuming open, the link's identity must match the stored recipient fingerprint.
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

function limitOf(kind: RequestKind, limits: RequestLimits): number {
	const max = kind === 'text' ? limits.maxTextBytes : limits.maxFileBytes;
	if (!Number.isSafeInteger(max) || max < 0) {
		invalid();
	}
	return max;
}

function assertLimit(size: number, kind: RequestKind, limits: RequestLimits): void {
	if (size > limitOf(kind, limits)) {
		invalid();
	}
}

// Fails the stream (cancelling the body) as soon as plaintext passes the limit.
function capped(
	stream: ReadableStream<Uint8Array>,
	kind: RequestKind,
	limits: RequestLimits
): ReadableStream<Uint8Array> {
	let max: number;
	try {
		max = limitOf(kind, limits);
	} catch (error) {
		stream.cancel().catch(() => {
			// Best-effort cancellation if the stream was already cancelled or consumed.
		});
		throw error;
	}
	let seen = 0;
	return stream.pipeThrough(
		new TransformStream<Uint8Array, Uint8Array>({
			transform(chunk, controller) {
				seen += chunk.length;
				if (seen > max) {
					controller.error(new Error('Invalid request encryption data.'));
					return;
				}
				controller.enqueue(chunk);
			}
		})
	);
}

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
	if (
		filename.length < 1 ||
		filename.length > NAME_MAX_BYTES ||
		decoder.decode(filename) !== file.name
	) {
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

// Resolves only after the final chunk authenticates; a file Blob is always
// application/octet-stream so it cannot render in the Flick origin.
export async function decryptRequestPayload(
	identity: string,
	opened: OpenedRequestBody,
	limits: RequestLimits
): Promise<OpenedRequestContent> {
	try {
		if (opened.kind === 'text') {
			if (opened.encryptedFilename) {
				invalid();
			}
			const decryptedStream = await decryptWith(identity, opened.body);
			const plaintext = new Uint8Array(
				await new Response(capped(decryptedStream, 'text', limits)).arrayBuffer()
			);
			return { kind: 'text', text: decoder.decode(plaintext) };
		}
		if (opened.kind !== 'file' || !opened.encryptedFilename) {
			invalid();
		}
		const decryptedStream = await decryptWith(identity, opened.body);
		const decrypted = await new Response(capped(decryptedStream, 'file', limits)).blob();
		const name = await decryptWith(identity, opened.encryptedFilename);
		if (name.length < 1 || name.length > NAME_MAX_BYTES) {
			invalid();
		}
		const filename = decoder.decode(name);
		return {
			kind: 'file',
			blob: decrypted.slice(0, decrypted.size, 'application/octet-stream'),
			filename
		};
	} catch (error) {
		try {
			await opened.body.cancel();
		} catch {
			// Ignore cancellation errors when cancelling a failed or consumed body.
		}
		throw error;
	}
}

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

export function utf8ByteLength(text: string): number {
	return encoder.encode(text).length;
}

// Single-purpose keypair: the age file stays on the request row; `memoKey` stays only in the
// submission URL fragment (#mk=).
export async function sealRequestNote(
	note: string
): Promise<{ encryptedNote: string; memoKey: string }> {
	if (!note.trim()) {
		invalid();
	}
	const bytes = encoder.encode(note);
	if (bytes.length > REQUEST_NOTE_MAX_BYTES || decoder.decode(bytes) !== note) {
		invalid();
	}
	const memoKey = await generateSecretIdentity();
	const recipient = await recipientOf(memoKey);
	const sealed = await encryptToRecipient(recipient, bytes);
	if (sealed.length > ENCRYPTED_NOTE_MAX_BYTES) {
		invalid();
	}
	return { encryptedNote: bytesToBase64(sealed), memoKey };
}

export async function openRequestNote(
	memoKeyFragment: string,
	encryptedNote: string
): Promise<string> {
	const memoKey = decodeKeyFragment(`key=${memoKeyFragment}`);
	if (!memoKey) {
		invalid();
	}
	let bytes: Uint8Array;
	try {
		bytes = base64ToBytes(encryptedNote);
	} catch {
		invalid();
	}
	if (
		bytes.length === 0 ||
		bytes.length > ENCRYPTED_NOTE_MAX_BYTES ||
		bytesToBase64(bytes) !== encryptedNote
	) {
		invalid();
	}
	const decrypted = await decryptWith(memoKey, bytes);
	if (decrypted.length === 0 || decrypted.length > REQUEST_NOTE_MAX_BYTES) {
		invalid();
	}
	return decoder.decode(decrypted);
}
