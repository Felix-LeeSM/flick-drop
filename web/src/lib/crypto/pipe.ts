import { t } from '#lib/i18n/index.js';
import { decryptWith, encryptToRecipient, generateSecretIdentity, recipientOf } from './age';
import { base64ToBytes, bytesToBase64 } from './encoding';

export const PIPE_CODE_LENGTH = 6;

export function normalizePipeCode(raw: string): string {
	const cleaned = raw.trim().replace(/[-\s]/g, '');
	if (cleaned.length !== PIPE_CODE_LENGTH || !/^\d{6}$/.test(cleaned)) {
		throw new Error('Code must be a 6-digit number');
	}
	return cleaned;
}

export function formatPipeCode(raw: string): string {
	const normalized = normalizePipeCode(raw);
	return `${normalized.slice(0, 3)}-${normalized.slice(3)}`;
}

/** The identity stays in this browser's memory; the API relays a stream it cannot decrypt. */
export async function createPipeReceiverKey(): Promise<{ identity: string; recipient: string }> {
	const identity = await generateSecretIdentity();
	return { identity, recipient: await recipientOf(identity) };
}

export async function encryptPipeFilename(recipient: string, filename: string): Promise<string> {
	return bytesToBase64(await encryptToRecipient(recipient, new TextEncoder().encode(filename)));
}

export async function decryptPipeFilename(identity: string, encrypted: string): Promise<string> {
	const plaintext = await decryptWith(identity, base64ToBytes(encrypted));
	return new TextDecoder('utf-8', { fatal: true }).decode(plaintext);
}

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

export async function streamToText(stream: ReadableStream<Uint8Array>): Promise<string> {
	const bytes = await streamToBytes(stream);
	return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
}

/** `reason` is the API error code or a status fallback; branch on it, not on message text. */
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

/** The sender token is the only sender credential; it stays in browser memory. */
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

/** The API hands the recipient to the sender only. Returns this device's random 3-digit number. */
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
 * Retries one transient failure (5xx, proxy 502/524, dropped socket): usually a replica
 * stopping in a rolling update; another replica reads the same room from Valkey. Never retries an abort.
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

/** Each poll holds ≤25 s so proxies never see silence; the open poll keeps this device listed. */
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
	// The room keeps a connection 35 s without a poll, so fetchLongPoll's one retry is in time.
	while (true) {
		const response = await fetchLongPoll(fetcher, `${apiBase(baseUrl)}/api/pipe/status`, {
			method: 'GET',
			headers,
			signal
		});
		if (!response.ok) {
			const err = await pipeError(response, t().live.errors.statusFailed);
			// not_found after a successful join means the server dropped this connection (no poll for
			// 35 s, e.g. a sleeping tab), not a decline: the code may be entered again.
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

// keepalive lets the request outlive a closing tab, so the sender's row goes at once, not after 35 s.
export async function leavePipe(
	baseUrl: string,
	code: string,
	connectionId: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	const response = await fetcher(`${apiBase(baseUrl)}/api/pipe/leave`, {
		method: 'POST',
		headers: {
			'X-Flick-Pipe-Code': normalizePipeCode(code),
			'X-Flick-Pipe-Connection': connectionId
		},
		keepalive: true
	});
	if (!response.ok) {
		throw await pipeError(response, t().live.errors.actionFailed);
	}
}

export type PipePendingConnection = {
	connectionId: string;
	index: number;
	verifyCode: string;
	device: string;
	recipient: string;
	connectedSeconds: number;
};

export type PipeWaitResult = {
	status: 'waiting' | 'ready';
	version: number;
	recipient?: string;
	connections: PipePendingConnection[];
};

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
		throw await pipeError(response, t().live.errors.actionFailed);
	}
}

export function selectPipeConnection(
	baseUrl: string,
	code: string,
	senderToken: string,
	connectionId: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	return senderAction({ path: 'select', baseUrl, code, senderToken, connectionId, fetcher });
}

export function declinePipeConnection(
	baseUrl: string,
	code: string,
	senderToken: string,
	connectionId: string,
	fetcher: typeof fetch = fetch
): Promise<void> {
	return senderAction({ path: 'decline', baseUrl, code, senderToken, connectionId, fetcher });
}

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

	// Chromium streams request bodies only over HTTP/2 and a failed fetch consumes the stream,
	// so buffer for non-https targets (plaintext HTTP/1.1 local dev).
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
