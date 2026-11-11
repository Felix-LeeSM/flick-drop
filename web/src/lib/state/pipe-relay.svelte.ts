import { onDestroy } from 'svelte';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import {
	cancelPipe,
	computePipePassphrase,
	declinePipeConnection,
	encryptPipeStream,
	initPipeRendezvous,
	type PipePendingConnection,
	selectPipeConnection,
	sendPipeStream,
	streamToBytes,
	waitForPipeUpdate
} from '#lib/crypto/pipe.js';

// The plaintext payload the page hands over when the transfer actually runs.
// `kind` and `filename` ride the send headers; encryption happens here, on
// the fly.
export type PipePayload = {
	kind: 'text' | 'file';
	stream: ReadableStream<Uint8Array>;
	filename?: string;
};

export type PipeRelayOptions = {
	// Both are read at send time, not at start time, so the page answers from
	// its live form state. The extra passphrase never leaves browser memory.
	getPayload: () => PipePayload;
	getExtraPassphrase: () => string;
};

/** One receiver waiting for the sender's pick, as the waiting panel lists it. */
export type PendingReceiver = {
	connectionId: string;
	index: number;
	verifyCode: string;
	device: string;
	/** Local clock time the connection joined, for the "12s ago" label. */
	joinedAt: number;
};

// Keeps each known receiver's joinedAt so its relative time does not jitter
// between polls; new receivers are placed from the server's connected_seconds.
function mergePending(
	previous: PendingReceiver[],
	connections: PipePendingConnection[],
	now: number
): PendingReceiver[] {
	const known = new Map(previous.map((receiver) => [receiver.connectionId, receiver.joinedAt]));
	return connections.map((connection) => ({
		connectionId: connection.connectionId,
		index: connection.index,
		verifyCode: connection.verifyCode,
		device: connection.device,
		joinedAt: known.get(connection.connectionId) ?? now - connection.connectedSeconds * 1000
	}));
}

/**
 * One sender-side live pipe relay session: rendezvous, the receiver waiting
 * room (confirmation on) or first-connector wait (off), and the encrypted
 * stream. All state lives in browser memory only — cancel or abort wipes it.
 */
export function createPipeRelay(options: PipeRelayOptions) {
	let activeCode = $state<string | null>(null);
	let requireConfirmation = $state(false);
	let pending = $state<PendingReceiver[]>([]);
	let statusText = $state('Waiting for receiver to enter code...');
	let progressPercent = $state(0);
	let completed = $state(false);
	let failed = $state(false);
	let expiresInSeconds = $state(180);
	// The sender token and salt never render; they stay out of reactive state.
	let senderToken = '';
	let salt = '';
	// True once the sender committed to one receiver; polling stops.
	let sending = false;
	let abortController: AbortController | null = null;

	function fail(err: unknown, fallback: string): void {
		statusText = err instanceof Error ? err.message : fallback;
		failed = true;
	}

	async function streamPayload(code: string, signal: AbortSignal): Promise<void> {
		try {
			const passphrase = computePipePassphrase(code, salt, options.getExtraPassphrase());
			const payload = options.getPayload();

			statusText = 'Encrypting stream on-the-fly (age format)...';
			progressPercent = 25;
			const encryptedStream = await encryptPipeStream(payload.stream, passphrase);

			statusText = 'Streaming encrypted bytes directly to receiver...';
			progressPercent = 50;

			let encryptedFilename: string | undefined;
			if (payload.filename) {
				const filenameBytes = new TextEncoder().encode(payload.filename);
				const encryptedFilenameStream = await encryptPipeStream(filenameBytes, passphrase);
				const filenameCipherBytes = await streamToBytes(encryptedFilenameStream);
				encryptedFilename = btoa(String.fromCharCode(...filenameCipherBytes));
			}

			await sendPipeStream({
				baseUrl: DEFAULT_API_BASE_URL,
				code,
				senderToken,
				stream: encryptedStream,
				kind: payload.kind,
				encryptedFilename,
				fetcher: fetch,
				signal
			});

			progressPercent = 100;
			completed = true;
			statusText = 'Transfer complete!';
		} catch (err) {
			if (!signal.aborted) {
				fail(err, 'Streaming failed');
			}
		}
	}

	// A poll that lands after the sender picked a device (or cancelled) is stale.
	async function applyUpdate(
		update: Awaited<ReturnType<typeof waitForPipeUpdate>>,
		code: string,
		signal: AbortSignal
	): Promise<void> {
		if (sending || signal.aborted) {
			return;
		}
		if (requireConfirmation) {
			pending = mergePending(pending, update.connections, Date.now());
			return;
		}
		if (update.status === 'ready') {
			sending = true;
			await streamPayload(code, signal);
		}
	}

	// Long-polls the session until the sender commits to a receiver. With
	// confirmation on it keeps the waiting list fresh; with it off it streams as
	// soon as the first receiver is attached.
	async function poll(code: string, signal: AbortSignal): Promise<void> {
		let since = 0;
		try {
			while (!(signal.aborted || sending)) {
				const update = await waitForPipeUpdate(
					DEFAULT_API_BASE_URL,
					code,
					senderToken,
					since,
					fetch,
					signal
				);
				since = update.version;
				await applyUpdate(update, code, signal);
			}
		} catch (err) {
			if (!signal.aborted && !sending) {
				fail(err, 'Live transfer failed');
			}
		}
	}

	async function start(confirmationRequired: boolean): Promise<void> {
		abortController?.abort();
		const controller = new AbortController();
		abortController = controller;
		sending = false;

		try {
			statusText = 'Initializing live transfer session...';
			const session = await initPipeRendezvous(DEFAULT_API_BASE_URL, confirmationRequired, fetch);
			if (controller.signal.aborted) {
				return;
			}
			activeCode = session.code;
			senderToken = session.senderToken;
			salt = session.salt;
			requireConfirmation = session.requireConfirmation;
			expiresInSeconds = session.expiresIn;
			pending = [];
			failed = false;
			completed = false;
			progressPercent = 0;
			statusText = requireConfirmation
				? 'Waiting for receivers... (confirmation required)'
				: 'Waiting for receiver to enter code...';
			await poll(session.code, controller.signal);
		} catch (err) {
			if (!controller.signal.aborted) {
				fail(err, 'Live transfer failed');
			}
		}
	}

	/** Sends to one waiting receiver; every other waiting receiver is turned away. */
	async function sendTo(connectionId: string): Promise<void> {
		const code = activeCode;
		const signal = abortController?.signal;
		if (code === null || signal === undefined || sending) {
			return;
		}
		sending = true;
		statusText = 'Sending to the chosen device...';
		try {
			await selectPipeConnection(DEFAULT_API_BASE_URL, code, senderToken, connectionId, fetch);
		} catch (err) {
			if (signal.aborted) {
				return;
			}
			// The device left or was settled meanwhile: keep the session and the list.
			sending = false;
			statusText = err instanceof Error ? err.message : 'That device is no longer waiting.';
			void poll(code, signal);
			return;
		}
		pending = [];
		await streamPayload(code, signal);
	}

	/** Turns one waiting receiver away; its slot reopens and the code stays live. */
	async function deny(connectionId: string): Promise<void> {
		const code = activeCode;
		if (code === null) {
			return;
		}
		pending = pending.filter((receiver) => receiver.connectionId !== connectionId);
		try {
			await declinePipeConnection(DEFAULT_API_BASE_URL, code, senderToken, connectionId, fetch);
		} catch {
			// The device already left or the session ended; the next poll shows the truth.
		}
	}

	function cancel(): void {
		const code = activeCode;
		const token = senderToken;
		abortController?.abort();
		abortController = null;
		// Burn the session on the server so waiting receivers learn at once. A
		// stream in flight is stopped by the abort above, not by this call.
		if (code !== null && token !== '' && !completed && progressPercent === 0) {
			void cancelPipe(DEFAULT_API_BASE_URL, code, token, fetch).catch(() => undefined);
		}
		activeCode = null;
		senderToken = '';
		salt = '';
		sending = false;
		pending = [];
		statusText = '';
		progressPercent = 0;
		completed = false;
		failed = false;
	}

	onDestroy(() => {
		abortController?.abort();
	});

	return {
		get activeCode() {
			return activeCode;
		},
		get requireConfirmation() {
			return requireConfirmation;
		},
		get pending() {
			return pending;
		},
		get statusText() {
			return statusText;
		},
		get progressPercent() {
			return progressPercent;
		},
		get completed() {
			return completed;
		},
		get failed() {
			return failed;
		},
		get expiresInSeconds() {
			return expiresInSeconds;
		},
		start,
		sendTo,
		deny,
		cancel
	};
}
