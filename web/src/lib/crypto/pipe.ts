import { Decrypter, Encrypter, type ReadableStreamWithSize } from 'age-encryption';

export const PIPE_CODE_LENGTH = 6;

/**
 * Normalizes user-entered pipe code by trimming whitespace and removing hyphens.
 * Throws an Error if the resulting code is not exactly 6 ASCII digits.
 */
export function normalizePipeCode(raw: string): string {
	const cleaned = raw.trim().replace(/[-\s]/g, '');
	if (cleaned.length !== PIPE_CODE_LENGTH || !/^\d{6}$/.test(cleaned)) {
		throw new Error('Code must be a 6-digit number');
	}
	return cleaned;
}

/**
 * Formats a 6-digit code into a human-readable "XXX-XXX" string.
 */
export function formatPipeCode(raw: string): string {
	const normalized = normalizePipeCode(raw);
	return `${normalized.slice(0, 3)}-${normalized.slice(3)}`;
}

/**
 * Derives the symmetric encryption passphrase combining the 6-digit code,
 * the server-issued cryptographic salt, and an optional secret passphrase.
 */
export function computePipePassphrase(
	code: string,
	salt: string,
	optionalPassphrase?: string
): string {
	const normalized = normalizePipeCode(code);
	const base = `${normalized}:${salt.trim()}`;
	if (optionalPassphrase && optionalPassphrase.trim() !== '') {
		return `${base}:${optionalPassphrase.trim()}`;
	}
	return base;
}

/**
 * Encrypts an input stream, buffer, or string to an age-formatted ciphertext stream
 * using symmetric passphrase encryption.
 */
export async function encryptPipeStream(
	input: ReadableStream<Uint8Array> | Uint8Array | string,
	passphrase: string
): Promise<ReadableStreamWithSize> {
	if (passphrase === '') {
		throw new Error('Passphrase cannot be empty');
	}

	const encrypter = new Encrypter();
	encrypter.setPassphrase(passphrase);

	let stream: ReadableStream<Uint8Array>;
	if (typeof input === 'string') {
		const bytes = new TextEncoder().encode(input);
		stream = new ReadableStream({
			start(controller) {
				controller.enqueue(bytes);
				controller.close();
			}
		});
	} else if (input instanceof Uint8Array) {
		stream = new ReadableStream({
			start(controller) {
				controller.enqueue(input);
				controller.close();
			}
		});
	} else {
		stream = input;
	}

	return await encrypter.encrypt(stream);
}

/**
 * Decrypts an age-formatted ciphertext stream or byte array using the derived passphrase.
 * Authenticates each 64 KiB chunk on the fly; fails if wrong passphrase or corrupted chunk.
 */
export async function decryptPipeStream(
	input: ReadableStream<Uint8Array> | Uint8Array,
	passphrase: string
): Promise<ReadableStream<Uint8Array>> {
	if (passphrase === '') {
		throw new Error('Passphrase cannot be empty');
	}

	const decrypter = new Decrypter();
	decrypter.addPassphrase(passphrase);

	let stream: ReadableStream<Uint8Array>;
	if (input instanceof Uint8Array) {
		stream = new ReadableStream({
			start(controller) {
				controller.enqueue(input);
				controller.close();
			}
		});
	} else {
		stream = input;
	}

	return await decrypter.decrypt(stream);
}

/**
 * Helper to drain a ReadableStream into a single Uint8Array.
 */
export async function streamToBytes(stream: ReadableStream<Uint8Array>): Promise<Uint8Array> {
	const reader = stream.getReader();
	const chunks: Uint8Array[] = [];
	let totalLength = 0;

	while (true) {
		const { done, value } = await reader.read();
		if (done) {
			break;
		}
		if (value) {
			chunks.push(value);
			totalLength += value.length;
		}
	}

	const result = new Uint8Array(totalLength);
	let offset = 0;
	for (const chunk of chunks) {
		result.set(chunk, offset);
		offset += chunk.length;
	}
	return result;
}

/**
 * Helper to drain a ReadableStream into a UTF-8 string.
 */
export async function streamToText(stream: ReadableStream<Uint8Array>): Promise<string> {
	const bytes = await streamToBytes(stream);
	return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
}

/**
 * A failed pipe call. `reason` is the API's error code (`declined`,
 * `room_full`, `not_selected`, …) or a status-derived fallback, so callers
 * branch on it instead of on message text.
 */
export class PipeRelayError extends Error {
	readonly reason: string;
	readonly status: number;

	constructor(reason: string, status: number, message: string) {
		super(message);
		this.name = 'PipeRelayError';
		this.reason = reason;
		this.status = status;
	}
}

const PIPE_ERROR_MESSAGES: Record<string, string> = {
	not_found: 'Invalid or expired 6-digit code.',
	declined: 'The sender declined this device.',
	room_full: 'Too many devices are trying to connect. Ask the sender for a new code.',
	already_connected: 'Another device is already receiving this transfer.',
	not_selected: 'The sender chose another device.',
	cancelled: 'The sender cancelled this code.',
	expired: 'This code expired.',
	closed: 'This transfer session has ended.',
	streaming: 'The transfer is already streaming.',
	rate_limited: 'Too many attempts. Wait a minute and try again.',
	capacity_exceeded: 'Server is at maximum capacity for live transfers. Please try again later.',
	service_unavailable: 'Live transfer is currently disabled on this server.'
};

function statusReason(status: number): string {
	switch (status) {
		case 404:
			return 'not_found';
		case 429:
			return 'rate_limited';
		case 503:
			return 'service_unavailable';
		default:
			return String(status);
	}
}

async function pipeError(response: Response, fallback: string): Promise<PipeRelayError> {
	let reason = '';
	try {
		const body = (await response.json()) as { error?: { code?: unknown } };
		reason = typeof body.error?.code === 'string' ? body.error.code : '';
	} catch {
		// A non-JSON body (proxy error page, empty body) falls back to the status.
	}
	if (reason === '') {
		reason = statusReason(response.status);
	}
	const message = PIPE_ERROR_MESSAGES[reason] ?? `${fallback} (${response.status})`;
	return new PipeRelayError(reason, response.status, message);
}

function apiBase(baseUrl: string): string {
	return baseUrl.replace(/\/+$/, '');
}

export type PipeInitResult = {
	code: string;
	salt: string;
	expiresIn: number;
	senderToken: string;
	requireConfirmation: boolean;
};

/**
 * Calls POST /api/pipe/init. The sender token it returns is the only credential
 * for the sender-side calls; it stays in browser memory.
 */
export async function initPipeRendezvous(
	baseUrl: string,
	requireConfirmation: boolean,
	fetcher: typeof fetch = fetch
): Promise<PipeInitResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/init`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ require_confirmation: requireConfirmation })
	});
	if (!response.ok) {
		throw await pipeError(response, 'Failed to initialize transfer session');
	}

	const data = (await response.json()) as {
		code: string;
		salt: string;
		expires_in: number;
		sender_token: string;
		require_confirmation: boolean;
	};
	return {
		code: data.code,
		salt: data.salt,
		expiresIn: data.expires_in,
		senderToken: data.sender_token,
		requireConfirmation: data.require_confirmation
	};
}

export type PipeJoinResult = {
	connectionId: string;
	requireConfirmation: boolean;
	/** Present only when the sender requires confirmation. */
	verifyCode?: string;
	device?: string;
	expiresIn: number;
};

/**
 * Calls POST /api/pipe/join with the 6-digit code. With confirmation on, the
 * server returns this device's own random 3-digit number to read to the sender.
 */
export async function joinPipe(
	baseUrl: string,
	code: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<PipeJoinResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/join`, {
		method: 'POST',
		headers: { 'X-Flick-Pipe-Code': normalizePipeCode(code) },
		signal
	});
	if (!response.ok) {
		throw await pipeError(response, 'Join failed');
	}
	const data = (await response.json()) as {
		connection_id: string;
		require_confirmation: boolean;
		verify_code?: string;
		device?: string;
		expires_in: number;
	};
	return {
		connectionId: data.connection_id,
		requireConfirmation: data.require_confirmation,
		verifyCode: data.verify_code,
		device: data.device,
		expiresIn: data.expires_in
	};
}

export type PipePendingConnection = {
	connectionId: string;
	index: number;
	verifyCode: string;
	device: string;
	connectedSeconds: number;
};

export type PipeWaitResult = {
	status: 'waiting' | 'ready';
	version: number;
	connections: PipePendingConnection[];
};

/**
 * Long-polls GET /api/pipe/wait. Resolves as soon as the session differs from
 * `since`, or after the server's hold time with the unchanged view.
 */
export async function waitForPipeUpdate(
	baseUrl: string,
	code: string,
	senderToken: string,
	since: number,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<PipeWaitResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/wait?since=${since}`, {
		method: 'GET',
		headers: {
			'X-Flick-Pipe-Code': normalizePipeCode(code),
			'X-Flick-Pipe-Sender': senderToken
		},
		signal
	});
	if (!response.ok) {
		throw await pipeError(response, 'Wait failed');
	}
	const data = (await response.json()) as {
		status: 'waiting' | 'ready';
		version: number;
		connections: {
			connection_id: string;
			index: number;
			verify_code: string;
			device: string;
			connected_seconds: number;
		}[];
	};
	return {
		status: data.status,
		version: data.version,
		connections: data.connections.map((connection) => ({
			connectionId: connection.connection_id,
			index: connection.index,
			verifyCode: connection.verify_code,
			device: connection.device,
			connectedSeconds: connection.connected_seconds
		}))
	};
}

type SenderAction = {
	path: 'select' | 'decline' | 'cancel';
	baseUrl: string;
	code: string;
	senderToken: string;
	connectionId?: string;
	fetcher: typeof fetch;
};

async function senderAction(action: SenderAction): Promise<void> {
	const headers: Record<string, string> = {
		'X-Flick-Pipe-Code': normalizePipeCode(action.code),
		'X-Flick-Pipe-Sender': action.senderToken
	};
	if (action.connectionId !== undefined) {
		headers['X-Flick-Pipe-Connection'] = action.connectionId;
	}
	const response = await action.fetcher(`${apiBase(action.baseUrl)}/api/pipe/${action.path}`, {
		method: 'POST',
		headers
	});
	if (!response.ok) {
		throw await pipeError(response, `${action.path} failed`);
	}
}

/** Calls POST /api/pipe/select: the transfer goes to this connection only. */
export function selectPipeConnection(
	baseUrl: string,
	code: string,
	senderToken: string,
	connectionId: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	return senderAction({ path: 'select', baseUrl, code, senderToken, connectionId, fetcher });
}

/** Calls POST /api/pipe/decline: ends this connection; the session stays open. */
export function declinePipeConnection(
	baseUrl: string,
	code: string,
	senderToken: string,
	connectionId: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	return senderAction({ path: 'decline', baseUrl, code, senderToken, connectionId, fetcher });
}

/** Calls POST /api/pipe/cancel: burns the whole session before streaming starts. */
export function cancelPipe(
	baseUrl: string,
	code: string,
	senderToken: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	return senderAction({ path: 'cancel', baseUrl, code, senderToken, fetcher });
}

export type SendPipeOptions = {
	baseUrl: string;
	code: string;
	senderToken: string;
	stream: ReadableStream<Uint8Array>;
	kind?: 'text' | 'file';
	encryptedFilename?: string;
	fetcher?: typeof fetch;
	signal?: AbortSignal;
};

/**
 * Streams encrypted bytes to POST /api/pipe/send.
 */
export async function sendPipeStream(options: SendPipeOptions): Promise<number> {
	const {
		baseUrl,
		code,
		senderToken,
		stream,
		kind,
		encryptedFilename,
		fetcher = fetch,
		signal
	} = options;
	const normalized = apiBase(baseUrl);

	const headers: Record<string, string> = {
		'X-Flick-Pipe-Code': normalizePipeCode(code),
		'X-Flick-Pipe-Sender': senderToken,
		'Content-Type': 'application/octet-stream'
	};
	if (kind) {
		headers['Flick-Kind'] = kind;
	}
	if (encryptedFilename) {
		headers['Flick-Encrypted-Filename'] = encryptedFilename;
	}

	// Chromium only allows streaming request bodies over HTTP/2; against a
	// plaintext HTTP/1.1 API (local dev) the fetch fails with
	// ERR_ALPN_NEGOTIATION_FAILED, and the rejected fetch has already consumed
	// the stream. So buffer the encrypted bytes up front whenever the target is
	// not https — browsers negotiate HTTP/2 (or let Chromium stream) on https
	// only, which is every publicly deployed Flick target.
	const streamRequestBody = normalized.startsWith('https');
	const body: BodyInit = streamRequestBody
		? stream
		: new Blob([(await streamToBytes(stream)) as Uint8Array<ArrayBuffer>]);

	const init: RequestInit = { method: 'POST', headers, body, signal };
	if (streamRequestBody) {
		// @ts-expect-error duplex option is standard in Fetch spec for streaming uploads
		init.duplex = 'half';
	}

	const response = await fetcher(`${normalized}/api/pipe/send`, init);

	if (!response.ok) {
		if (response.status === 502) {
			throw new PipeRelayError(
				'receiver_disconnected',
				502,
				'Receiver disconnected during live transfer.'
			);
		}
		throw await pipeError(response, 'Send failed');
	}

	const data = (await response.json()) as { status: string; bytes_transferred: number };
	return data.bytes_transferred;
}

export type ReceivePipeResult = {
	stream: ReadableStream<Uint8Array>;
	salt: string;
	kind?: string;
	encryptedFilename?: string;
};

/**
 * Calls GET /api/pipe/receive on a joined connection. Resolves with the live
 * stream once the sender picks this device (with confirmation off: once this
 * device connected first and the sender started); rejects with a
 * PipeRelayError otherwise.
 */
export async function receivePipeStream(
	baseUrl: string,
	code: string,
	connectionId: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<ReceivePipeResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/receive`, {
		method: 'GET',
		headers: {
			'X-Flick-Pipe-Code': normalizePipeCode(code),
			'X-Flick-Pipe-Connection': connectionId
		},
		signal
	});

	if (!response.ok) {
		throw await pipeError(response, 'Receive failed');
	}

	if (!response.body) {
		throw new Error('Server response body is empty');
	}

	const salt = response.headers.get('X-Flick-Pipe-Salt') ?? '';
	const kind = response.headers.get('Flick-Kind') ?? undefined;
	const encryptedFilename = response.headers.get('Flick-Encrypted-Filename') ?? undefined;

	return {
		stream: response.body,
		salt,
		kind,
		encryptedFilename
	};
}
