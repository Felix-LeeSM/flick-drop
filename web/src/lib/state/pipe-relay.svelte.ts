import { onDestroy } from 'svelte';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import { encryptToRecipient } from '#lib/crypto/age.js';
import {
	cancelPipe,
	declinePipeConnection,
	encryptPipeFilename,
	initPipeRendezvous,
	type PipePendingConnection,
	PipeRelayError,
	selectPipeConnection,
	sendPipeStream,
	waitForPipeUpdate
} from '#lib/crypto/pipe.js';
import { type Messages, t } from '#lib/i18n/index.js';

type RelayStatus = keyof Messages['live']['relay'];

// The plaintext payload the page hands over when the transfer actually runs.
// `kind` and `filename` ride the send headers; encryption happens here, on
// the fly.
export type PipePayload = {
	kind: 'text' | 'file';
	stream: ReadableStream<Uint8Array>;
	filename?: string;
};

export type PipeRelayOptions = {
	// Read at send time, not at start time, so the page answers from its live
	// form state. A code transfer takes no passphrase: the stream is encrypted to
	// the chosen receiver's age recipient.
	getPayload: () => PipePayload;
};

/** One receiver waiting for the sender's pick, as the waiting panel lists it. */
export type PendingReceiver = {
	connectionId: string;
	index: number;
	verifyCode: string;
	device: string;
	/** The age recipient this device generated; the stream is encrypted to it if picked. */
	recipient: string;
	/** Local clock time the connection joined, for the "12s ago" label. */
	joinedAt: number;
};

// Keeps each known receiver's joinedAt so its relative time does not jitter
// between polls; new receivers are placed from the server's connected_seconds.
// Rows the sender already settled locally stay out even when a poll that was
// in flight during the decision still lists them.
function mergePending(
	previous: PendingReceiver[],
	connections: PipePendingConnection[],
	settled: ReadonlySet<string>,
	now: number
): PendingReceiver[] {
	const known = new Map(previous.map((receiver) => [receiver.connectionId, receiver.joinedAt]));
	return connections
		.filter((connection) => !settled.has(connection.connectionId))
		.map((connection) => ({
			connectionId: connection.connectionId,
			index: connection.index,
			verifyCode: connection.verifyCode,
			device: connection.device,
			recipient: connection.recipient,
			joinedAt: known.get(connection.connectionId) ?? now - connection.connectedSeconds * 1000
		}));
}

/**
 * One sender-side live pipe relay session: rendezvous, the receiver waiting
 * room, and the encrypted stream. All state lives in browser memory only —
 * cancel or abort wipes it.
 */
export function createPipeRelay(options: PipeRelayOptions) {
	let activeCode = $state<string | null>(null);
	let pending = $state<PendingReceiver[]>([]);
	// A relay status is read from the catalog on render so it follows a language
	// switch; an error message is fixed in the language it was created in.
	let status = $state<() => string>(() => t().live.relay.waitingForCode);
	let progressPercent = $state(0);
	let completed = $state(false);
	let failed = $state(false);
	let expiresInSeconds = $state(180);
	// The sender token never renders; it stays out of reactive state.
	let senderToken = '';
	// True once the sender committed to one receiver; the poll loop ends.
	let sending = false;
	// True while a Send click waits for /select; a second click is ignored and
	// the one poll loop keeps running, so a failed select needs no new loop.
	let selecting = false;
	// Rows the sender denied or picked in this session. A /wait response that
	// was already in flight when the sender decided must not bring them back.
	let settled = new Set<string>();
	let abortController: AbortController | null = null;

	function setStatus(key: RelayStatus): void {
		status = () => t().live.relay[key];
	}

	function setStatusFrom(err: unknown, fallback: RelayStatus): void {
		if (err instanceof Error) {
			const message = err.message;
			status = () => message;
			return;
		}
		setStatus(fallback);
	}

	function fail(err: unknown, fallback: RelayStatus): void {
		setStatusFrom(err, fallback);
		failed = true;
	}

	// Encrypts to the receiver's own age recipient, so only that browser's
	// in-memory identity can open the stream; the API relays ciphertext only.
	async function streamPayload(
		code: string,
		recipient: string,
		signal: AbortSignal
	): Promise<void> {
		try {
			const payload = options.getPayload();

			setStatus('encrypting');
			progressPercent = 25;
			const encryptedStream = await encryptToRecipient(recipient, payload.stream);

			setStatus('streaming');
			progressPercent = 50;

			const encryptedFilename = payload.filename
				? await encryptPipeFilename(recipient, payload.filename)
				: undefined;

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
			setStatus('complete');
		} catch (err) {
			if (!signal.aborted) {
				fail(err, 'streamingFailed');
			}
		}
	}

	// A poll that lands after the sender picked a device (or cancelled) is stale.
	function applyUpdate(
		update: Awaited<ReturnType<typeof waitForPipeUpdate>>,
		signal: AbortSignal
	): void {
		if (sending || signal.aborted) {
			return;
		}
		pending = mergePending(pending, update.connections, settled, Date.now());
	}

	// Long-polls the session to keep the waiting list fresh until the sender
	// commits to a receiver. `start` runs exactly one loop per session; nothing
	// else starts one.
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
				applyUpdate(update, signal);
			}
		} catch (err) {
			if (!signal.aborted && !sending) {
				fail(err, 'liveFailed');
			}
		}
	}

	async function start(): Promise<void> {
		abortController?.abort();
		const controller = new AbortController();
		abortController = controller;
		sending = false;
		selecting = false;
		settled = new Set();

		try {
			setStatus('initializing');
			const session = await initPipeRendezvous(DEFAULT_API_BASE_URL, fetch);
			if (controller.signal.aborted) {
				return;
			}
			activeCode = session.code;
			senderToken = session.senderToken;
			expiresInSeconds = session.expiresIn;
			pending = [];
			failed = false;
			completed = false;
			progressPercent = 0;
			setStatus('waitingForCode');
			await poll(session.code, controller.signal);
		} catch (err) {
			if (!controller.signal.aborted) {
				fail(err, 'liveFailed');
			}
		}
	}

	/** Sends to one waiting receiver; every other waiting receiver is turned away. */
	async function sendTo(connectionId: string): Promise<void> {
		const code = activeCode;
		const signal = abortController?.signal;
		if (code === null || signal === undefined || sending || selecting) {
			return;
		}
		const chosen = pending.find((receiver) => receiver.connectionId === connectionId);
		if (!chosen) {
			setStatus('noLongerWaiting');
			return;
		}
		selecting = true;
		setStatus('sendingChosen');
		try {
			await selectPipeConnection(DEFAULT_API_BASE_URL, code, senderToken, connectionId, fetch);
		} catch (err) {
			if (signal.aborted) {
				return;
			}
			// The device left or was settled meanwhile: keep the session and the
			// list. The running poll loop shows the next server view.
			selecting = false;
			setStatusFrom(err, 'noLongerWaiting');
			return;
		}
		selecting = false;
		sending = true;
		settled.add(connectionId);
		pending = [];
		await streamPayload(code, chosen.recipient, signal);
	}

	function restoreDenied(denied: PendingReceiver | undefined, connectionId: string): void {
		settled.delete(connectionId);
		if (denied && !pending.some((receiver) => receiver.connectionId === connectionId)) {
			pending = [...pending, denied].sort((a, b) => a.index - b.index);
		}
		setStatus('denyFailed');
	}

	/** Turns one waiting receiver away; its slot reopens and the code stays live. */
	async function deny(connectionId: string): Promise<void> {
		const code = activeCode;
		if (code === null) {
			return;
		}
		const denied = pending.find((receiver) => receiver.connectionId === connectionId);
		const session = settled;
		settled.add(connectionId);
		pending = pending.filter((receiver) => receiver.connectionId !== connectionId);
		try {
			await declinePipeConnection(DEFAULT_API_BASE_URL, code, senderToken, connectionId, fetch);
			if (session === settled && !sending) {
				setStatus('waitingForCode');
			}
		} catch (err) {
			// not_pending: the device already left or was settled, so the row stays gone.
			const alreadyGone = err instanceof PipeRelayError && err.reason === 'not_pending';
			// A new session (or cancel) replaced `settled` meanwhile; this row is not ours to restore.
			if (!alreadyGone && session === settled) {
				restoreDenied(denied, connectionId);
			}
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
		sending = false;
		selecting = false;
		settled = new Set();
		pending = [];
		status = () => '';
		progressPercent = 0;
		completed = false;
		failed = false;
	}

	// Leaving the page (the logo, another route) burns a waiting session too, so
	// receivers learn at once instead of waiting out the code's expiry.
	onDestroy(cancel);

	return {
		get activeCode() {
			return activeCode;
		},
		get pending() {
			return pending;
		},
		get statusText() {
			return status();
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
