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
 * Decrypts an age-formatted ciphertext stream using the derived passphrase.
 * Authenticates each 64 KiB chunk on the fly; fails if wrong passphrase or corrupted chunk.
 */
export async function decryptPipeStream(
	stream: ReadableStream<Uint8Array>,
	passphrase: string
): Promise<ReadableStream<Uint8Array>> {
	if (passphrase === '') {
		throw new Error('Passphrase cannot be empty');
	}

	const decrypter = new Decrypter();
	decrypter.addPassphrase(passphrase);

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

export type PipeInitResult = {
	code: string;
	salt: string;
	expiresIn: number;
};

/**
 * Calls POST /api/pipe/init to generate an uncolliding 6-digit code and 16-byte salt.
 */
export async function initPipeRendezvous(
	baseUrl: string,
	fetcher: typeof fetch = fetch
): Promise<PipeInitResult> {
	const normalized = baseUrl.replace(/\/+$/, '');
	const response = await fetcher(`${normalized}/api/pipe/init`, {
		method: 'POST'
	});

	if (!response.ok) {
		if (response.status === 429) {
			throw new Error('Server is at maximum capacity for live transfers. Please try again later.');
		}
		if (response.status === 503) {
			throw new Error('Live transfer is currently disabled on this server.');
		}
		throw new Error(`Failed to initialize transfer session (${response.status})`);
	}

	const data = (await response.json()) as { code: string; salt: string; expires_in: number };
	return {
		code: data.code,
		salt: data.salt,
		expiresIn: data.expires_in
	};
}

/**
 * Long-polls GET /api/pipe/wait with X-Flick-Pipe-Code until receiver connects.
 */
export async function waitForReceiver(
	baseUrl: string,
	code: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<void> {
	const normalizedCode = normalizePipeCode(code);
	const normalized = baseUrl.replace(/\/+$/, '');

	const response = await fetcher(`${normalized}/api/pipe/wait`, {
		method: 'GET',
		headers: {
			'X-Flick-Pipe-Code': normalizedCode
		},
		signal
	});

	if (!response.ok) {
		if (response.status === 404) {
			throw new Error('Transfer code not found or expired.');
		}
		if (response.status === 410) {
			throw new Error('Transfer session expired or burned.');
		}
		throw new Error(`Wait failed with status ${response.status}`);
	}
}

export type SendPipeOptions = {
	baseUrl: string;
	code: string;
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
	const { baseUrl, code, stream, kind, encryptedFilename, fetcher = fetch, signal } = options;
	const normalizedCode = normalizePipeCode(code);
	const normalized = baseUrl.replace(/\/+$/, '');

	const headers: Record<string, string> = {
		'X-Flick-Pipe-Code': normalizedCode,
		'Content-Type': 'application/octet-stream'
	};
	if (kind) {
		headers['Flick-Kind'] = kind;
	}
	if (encryptedFilename) {
		headers['Flick-Encrypted-Filename'] = encryptedFilename;
	}

	const response = await fetcher(`${normalized}/api/pipe/send`, {
		method: 'POST',
		headers,
		body: stream,
		// @ts-expect-error duplex option is standard in Fetch spec for streaming uploads
		duplex: 'half',
		signal
	});

	if (!response.ok) {
		if (response.status === 404) {
			throw new Error('Transfer session not found or expired.');
		}
		if (response.status === 502) {
			throw new Error('Receiver disconnected during live transfer.');
		}
		throw new Error(`Send failed with status ${response.status}`);
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
 * Connects to GET /api/pipe/receive and returns the live response stream and metadata headers.
 */
export async function receivePipeStream(
	baseUrl: string,
	code: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<ReceivePipeResult> {
	const normalizedCode = normalizePipeCode(code);
	const normalized = baseUrl.replace(/\/+$/, '');

	const response = await fetcher(`${normalized}/api/pipe/receive`, {
		method: 'GET',
		headers: {
			'X-Flick-Pipe-Code': normalizedCode
		},
		signal
	});

	if (!response.ok) {
		if (response.status === 404) {
			throw new Error('Invalid or expired 6-digit code.');
		}
		if (response.status === 409) {
			throw new Error('Another device is already receiving this transfer.');
		}
		if (response.status === 410) {
			throw new Error('Transfer code burned due to too many failed attempts.');
		}
		throw new Error(`Receive failed with status ${response.status}`);
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
