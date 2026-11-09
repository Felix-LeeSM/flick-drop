import { onDestroy } from 'svelte';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import {
	computePipePassphrase,
	deriveConfirmationCode,
	encryptPipeStream,
	initPipeRendezvous,
	sendPipeStream,
	streamToBytes,
	waitForReceiver
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

/**
 * One sender-side live pipe relay session: rendezvous, receiver wait,
 * optional confirmation, and the encrypted stream. All state lives in
 * browser memory only — cancel or abort wipes the session.
 */
export function createPipeRelay(options: PipeRelayOptions) {
	let activeCode = $state<string | null>(null);
	let salt = $state('');
	let statusText = $state('Waiting for receiver to enter code...');
	let progressPercent = $state(0);
	let completed = $state(false);
	let approvalStatus = $state<'idle' | 'pending' | 'approved' | 'rejected'>('idle');
	let matchCode = $state('482');
	let expiresInSeconds = $state(180);
	let abortController = $state<AbortController | null>(null);

	async function streamPayload(): Promise<void> {
		if (activeCode === null) {
			return;
		}
		const signal = abortController?.signal;

		try {
			const passphrase = computePipePassphrase(activeCode, salt, options.getExtraPassphrase());
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
				code: activeCode,
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
			if (signal?.aborted) {
				return;
			}
			statusText = err instanceof Error ? err.message : 'Streaming failed';
			approvalStatus = 'rejected';
		}
	}

	async function start(requireApproval: boolean): Promise<void> {
		abortController?.abort();
		abortController = new AbortController();
		const signal = abortController.signal;

		try {
			statusText = 'Initializing live transfer session...';
			const session = await initPipeRendezvous(DEFAULT_API_BASE_URL, fetch);
			activeCode = session.code;
			salt = session.salt;
			matchCode = deriveConfirmationCode(session.code);
			expiresInSeconds = session.expiresIn;
			approvalStatus = 'idle';
			completed = false;
			progressPercent = 0;
			statusText = requireApproval
				? 'Waiting for receiver to enter code... (confirmation required)'
				: 'Waiting for receiver to enter code...';

			await waitForReceiver(DEFAULT_API_BASE_URL, session.code, fetch, signal);

			if (requireApproval) {
				approvalStatus = 'pending';
				statusText = 'Receiver connected. Please confirm transfer.';
			} else {
				await streamPayload();
			}
		} catch (err) {
			if (signal.aborted) {
				return;
			}
			statusText = err instanceof Error ? err.message : 'Live transfer failed';
			approvalStatus = 'rejected';
		}
	}

	async function approve(): Promise<void> {
		approvalStatus = 'approved';
		statusText = 'Approved. Encrypting and streaming chunks...';
		await streamPayload();
	}

	function reject(): void {
		approvalStatus = 'rejected';
		statusText = 'Transfer denied. Session wiped from memory.';
		abortController?.abort();
	}

	function cancel(): void {
		abortController?.abort();
		activeCode = null;
		statusText = '';
		progressPercent = 0;
		completed = false;
		approvalStatus = 'idle';
	}

	onDestroy(() => {
		abortController?.abort();
	});

	return {
		get activeCode() {
			return activeCode;
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
		get approvalStatus() {
			return approvalStatus;
		},
		get matchCode() {
			return matchCode;
		},
		get expiresInSeconds() {
			return expiresInSeconds;
		},
		start,
		approve,
		reject,
		cancel
	};
}
