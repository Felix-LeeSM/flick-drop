import { t } from '#lib/i18n/index.js';
import { decryptWith, encryptToRecipient, generateSecretIdentity, recipientOf } from './age';
import { base64ToBytes, bytesToBase64 } from './encoding';

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
 * A fresh age identity for one receive attempt, and the recipient (public
 * half) that goes to the API on /join. The identity stays in this browser's
 * memory only: the sender encrypts to the recipient, so the API relays a
 * stream it cannot decrypt.
 */
export async function createPipeReceiverKey(): Promise<{ identity: string; recipient: string }> {
	const identity = await generateSecretIdentity();
	return { identity, recipient: await recipientOf(identity) };
}

/** The Flick-Encrypted-Filename header value: an age file to `recipient`, base64. */
export async function encryptPipeFilename(recipient: string, filename: string): Promise<string> {
	return bytesToBase64(await encryptToRecipient(recipient, new TextEncoder().encode(filename)));
}

/** Opens a Flick-Encrypted-Filename header value with this receiver's identity. */
export async function decryptPipeFilename(identity: string, encrypted: string): Promise<string> {
	const plaintext = await decryptWith(identity, base64ToBytes(encrypted));
	return new TextDecoder('utf-8', { fatal: true }).decode(plaintext);
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

// The message for an API error code in the display language, or undefined for a
// code the catalog does not know.
function pipeErrorMessage(reason: string): string | undefined {
	const reasons: Partial<Record<string, string>> = t().live.errors.reasons;
	return Object.hasOwn(reasons, reason) ? reasons[reason] : undefined;
}

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

// `fallback` names the failed call in the display language when the API code is unknown.
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
	const message = pipeErrorMessage(reason) ?? `${fallback} (${response.status})`;
	return new PipeRelayError(reason, response.status, message);
}

function apiBase(baseUrl: string): string {
	return baseUrl.replace(/\/+$/, '');
}

export type PipeInitResult = {
	code: string;
	expiresIn: number;
	senderToken: string;
};

/**
 * Calls POST /api/pipe/init. The sender token it returns is the only credential
 * for the sender-side calls; it stays in browser memory.
 */
export async function initPipeRendezvous(
	baseUrl: string,
	fetcher: typeof fetch = fetch
): Promise<PipeInitResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/init`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: '{}'
	});
	if (!response.ok) {
		throw await pipeError(response, t().live.errors.initFailed);
	}

	const data = (await response.json()) as {
		code: string;
		expires_in: number;
		sender_token: string;
	};
	return {
		code: data.code,
		expiresIn: data.expires_in,
		senderToken: data.sender_token
	};
}

export type PipeJoinResult = {
	connectionId: string;
	verifyCode: string;
	device: string;
	expiresIn: number;
};

/**
 * Calls POST /api/pipe/join with the 6-digit code and this device's age
 * recipient (`createPipeReceiverKey`), which the API hands to the sender only.
 * Returns this device's own random 3-digit number to read to the sender.
 */
export async function joinPipe(
	baseUrl: string,
	code: string,
	recipient: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<PipeJoinResult> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/join`, {
		method: 'POST',
		headers: { 'X-Flick-Pipe-Code': normalizePipeCode(code), 'Content-Type': 'application/json' },
		body: JSON.stringify({ recipient }),
		signal
	});
	if (!response.ok) {
		throw await pipeError(response, t().live.errors.joinFailed);
	}
	const data = (await response.json()) as {
		connection_id: string;
		verify_code: string;
		device: string;
		expires_in: number;
	};
	return {
		connectionId: data.connection_id,
		verifyCode: data.verify_code,
		device: data.device,
		expiresIn: data.expires_in
	};
}

/**
 * Fetches one long-poll (/status or /wait), retrying a single transient
 * failure: a 5xx from the API or a proxy (Cloudflare's 502 and 524), or a
 * socket the network dropped (fetch rejects with a TypeError). An API replica
 * stopping for a rolling update is the usual cause; the retry reaches another
 * replica, which reads the same waiting room from Valkey. A second failure in
 * a row is returned or thrown to the caller. An abort is never retried.
 */
async function fetchLongPoll(
	fetcher: typeof fetch,
	url: string,
	init: RequestInit
): Promise<Response> {
	try {
		const response = await fetcher(url, init);
		if (response.status < 500) {
			return response;
		}
		// Release the failed response's connection before the retry.
		await response.body?.cancel();
	} catch (err) {
		if (!(err instanceof TypeError)) {
			throw err;
		}
	}
	return fetcher(url, init);
}

/**
 * Long-polls GET /api/pipe/status until the sender picks this connection. Each
 * poll holds at most 25 seconds, so a proxy never sees a silent response, and
 * the open poll is what keeps this device in the sender's list. Resolves once
 * selected (open /receive next); rejects with a PipeRelayError when the device
 * was declined, not chosen, or the session ended.
 */
export async function waitForPipeSelection(
	baseUrl: string,
	code: string,
	connectionId: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<void> {
	const headers = {
		'X-Flick-Pipe-Code': normalizePipeCode(code),
		'X-Flick-Pipe-Connection': connectionId
	};
	// The connection stays in the room for 35 seconds without a poll, so the
	// one retry inside fetchLongPoll is still in time.
	while (true) {
		const response = await fetchLongPoll(fetcher, `${apiBase(baseUrl)}/api/pipe/status`, {
			method: 'GET',
			headers,
			signal
		});
		if (!response.ok) {
			const err = await pipeError(response, t().live.errors.statusFailed);
			// The join succeeded with this code, so not_found here means the server
			// dropped this connection (no poll for 35 seconds, e.g. a sleeping tab).
			// It is not a decline: the same code may be entered again.
			if (err.reason === 'not_found') {
				throw new PipeRelayError('dropped', response.status, t().live.errors.reasons.dropped);
			}
			throw err;
		}
		const data = (await response.json()) as { status: 'waiting' | 'selected' };
		if (data.status === 'selected') {
			return;
		}
	}
}

export type PipePendingConnection = {
	connectionId: string;
	index: number;
	verifyCode: string;
	device: string;
	/** The age recipient this device generated; the sender encrypts to it when picking this row. */
	recipient: string;
	connectedSeconds: number;
};

export type PipeWaitResult = {
	status: 'waiting' | 'ready';
	version: number;
	/** The attached receiver's age recipient, present once `status` is `ready`. */
	recipient?: string;
	connections: PipePendingConnection[];
};

/**
 * Long-polls GET /api/pipe/wait. Resolves as soon as the session differs from
 * `since`, or after the server's hold time with the unchanged view. One
 * transient failure is retried (`fetchLongPoll`).
 */
export async function waitForPipeUpdate(
	baseUrl: string,
	code: string,
	senderToken: string,
	since: number,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<PipeWaitResult> {
	const response = await fetchLongPoll(
		fetcher,
		`${apiBase(baseUrl)}/api/pipe/wait?since=${since}`,
		{
			method: 'GET',
			headers: {
				'X-Flick-Pipe-Code': normalizePipeCode(code),
				'X-Flick-Pipe-Sender': senderToken
			},
			signal
		}
	);
	if (!response.ok) {
		throw await pipeError(response, t().live.errors.waitFailed);
	}
	const data = (await response.json()) as {
		status: 'waiting' | 'ready';
		version: number;
		recipient?: string;
		connections: {
			connection_id: string;
			index: number;
			verify_code: string;
			device: string;
			recipient: string;
			connected_seconds: number;
		}[];
	};
	return {
		status: data.status,
		version: data.version,
		recipient: data.recipient,
		connections: data.connections.map((connection) => ({
			connectionId: connection.connection_id,
			index: connection.index,
			verifyCode: connection.verify_code,
			device: connection.device,
			recipient: connection.recipient,
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
		throw await pipeError(response, t().live.errors.actionFailed(action.path));
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
				t().live.errors.reasons.receiver_disconnected
			);
		}
		throw await pipeError(response, t().live.errors.sendFailed);
	}

	const data = (await response.json()) as { status: string; bytes_transferred: number };
	return data.bytes_transferred;
}

export type ReceivePipeResult = {
	stream: ReadableStream<Uint8Array>;
	kind?: string;
	encryptedFilename?: string;
};

/**
 * Calls GET /api/pipe/receive on a joined connection after `waitForPipeSelection`
 * resolved. Resolves with the live stream once the sender starts; rejects with
 * a PipeRelayError otherwise.
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
		throw await pipeError(response, t().live.errors.receiveFailed);
	}

	if (!response.body) {
		throw new Error(t().live.errors.emptyBody);
	}

	const kind = response.headers.get('Flick-Kind') ?? undefined;
	const encryptedFilename = response.headers.get('Flick-Encrypted-Filename') ?? undefined;

	return {
		stream: response.body,
		kind,
		encryptedFilename
	};
}
