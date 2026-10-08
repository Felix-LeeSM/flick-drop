<script lang="ts">
import { onMount, tick as render } from 'svelte';
import { defaultLimits, getVerifiedConfig } from '$lib/api/config';
import {
	createRequestApiClient,
	prepareLargeSubmission,
	prepareSubmission,
	RequestApiError,
	type RequestInstructions,
	type ReservedUpload,
	requestLimits,
	type Submission
} from '$lib/api/requests';
import { DEFAULT_API_BASE_URL, type FileUploadProgress, SecretApiError } from '$lib/api/secrets';
import DeliveryFrame from '$lib/components/DeliveryFrame.svelte';
import { Button } from '$lib/components/ui/button';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
import { Textarea } from '$lib/components/ui/textarea';
import { encryptRequestPayload, importRequestPublicKey } from '$lib/crypto/requests';
import { remainingSecondsFrom } from '$lib/lifetime';
import { submissionFromFragment } from '$lib/state/request-links';
import {
	type AttemptOutcome,
	cancelRequestUpload,
	finishRequestUpload,
	inspectRequestAttempt,
	uploadExpired
} from '$lib/state/request-upload';

let { requestId }: { requestId: string } = $props();
const api = createRequestApiClient();
let instructions = $state<RequestInstructions | null>(null);
let limits = $state(requestLimits(defaultLimits()));
let phase = $state<
	| 'loading'
	| 'ready'
	| 'submitting'
	| 'unknown'
	| 'accepted'
	| 'unavailable'
	| 'expired'
	| 'invalid'
>('loading');
let mode = $state<'text' | 'file'>('text');
let message = $state('');
let selected = $state<File | null>(null);
let fileInput = $state<HTMLInputElement | null>(null);
let error = $state('');
let resumed = $state(false);
let retryReady = $state(false);
let notice = $state('');
let progress = $state<FileUploadProgress | null>(null);
let reservation = $state<ReservedUpload | null>(null);
let reservationExpired = $state(false);
let cancelRequested = $state(false);
let busy = $state(false);
let now = $state(Date.now());
let heading = $state<HTMLHeadingElement | null>(null);
let token = '';
let fingerprint = '';
let publicKey: CryptoKey | null = null;
let pending = $state<Submission | null>(null);
let controller: AbortController | null = null;
let alive = false;
let configured = false;
const remaining = $derived(remainingSecondsFrom(instructions?.expires_at ?? '', now));
const terminal = $derived(['accepted', 'unavailable', 'expired', 'invalid'].includes(phase));
const title = $derived(
	phase === 'accepted'
		? 'Submission accepted'
		: hasExpired()
			? 'Request expired'
			: phase === 'unavailable'
				? 'Request unavailable'
				: 'Send a secret'
);

function clearInput(): void {
	message = '';
	selected = null;
	if (fileInput) {
		fileInput.value = '';
	}
}
function clear(): void {
	clearInput();
	pending = null;
	publicKey = null;
	retryReady = false;
	reservation = null;
	reservationExpired = false;
	progress = null;
	cancelRequested = false;
}
function clock(): void {
	now = Date.now();
	if (instructions && now >= Date.parse(instructions.expires_at) && !hasExpired()) {
		phase = 'expired';
		error = '';
		controller?.abort();
		clear();
	}
	if (reservation && now >= Date.parse(reservation.reservation_expires_at) && !reservationExpired) {
		reservationExpired = true;
		retryReady = false;
		if (phase === 'submitting') {
			controller?.abort();
			phase = 'unknown';
			busy = false;
		}
		if (!cancelRequested) {
			error = 'The upload reservation expired. Check the attempt before choosing another file.';
		}
	}
}
async function focus(): Promise<void> {
	await render();
	if (alive) {
		heading?.focus();
	}
}

function current(signal: AbortSignal): boolean {
	return alive && !signal.aborted;
}
function hasExpired(): boolean {
	return phase === 'expired';
}
async function pinKey(data: RequestInstructions, signal: AbortSignal): Promise<void> {
	try {
		const key = await importRequestPublicKey(data.public_key, fingerprint);
		if (current(signal)) {
			publicKey = key;
			phase = 'ready';
		}
	} catch {
		if (current(signal)) {
			phase = 'invalid';
			error =
				'The request key does not match this link. Ask the requester for a new submission link.';
		}
	}
}

function reportLoad(cause: unknown): void {
	if (cause instanceof RequestApiError && cause.status === 404) {
		phase = 'unavailable';
	}
	error =
		cause instanceof RequestApiError ? cause.message : 'Could not load the request. Try again.';
}
async function load(): Promise<void> {
	if (busy || !token || terminal || pending) {
		return;
	}
	busy = true;
	error = '';
	const request = new AbortController();
	controller = request;
	try {
		if (!configured) {
			limits = requestLimits(await getVerifiedConfig(DEFAULT_API_BASE_URL, fetch, request.signal));
			configured = true;
		}
		const data = await api.instructions(requestId, token, request.signal);
		if (!current(request.signal)) {
			return;
		}
		instructions = data;
		clock();
		if (hasExpired()) {
			return;
		}
		if (!data.can_submit) {
			phase = 'unavailable';
			return;
		}
		await pinKey(data, request.signal);
	} catch (cause) {
		if (current(request.signal)) {
			reportLoad(cause);
		}
	} finally {
		if (alive && controller === request) {
			busy = false;
		}
	}
}

function accepted(): void {
	phase = 'accepted';
	error = '';
	clear();
	void focus();
}

async function sendPending(request: AbortController): Promise<void> {
	if (!pending || !instructions) {
		return;
	}
	try {
		if (pending.ciphertext === undefined) {
			await api.submit(requestId, token, pending, request.signal);
		} else {
			await finishRequestUpload(
				api,
				requestId,
				token,
				pending,
				instructions.expires_at,
				request.signal,
				(next) => {
					if (current(request.signal)) {
						progress = next;
					}
				},
				(next) => {
					if (current(request.signal)) {
						reservation = next;
						reservationExpired = uploadExpired(next);
					}
				}
			);
		}
		if (current(request.signal)) {
			clock();
			if (!hasExpired()) {
				accepted();
			}
		}
	} catch (cause) {
		if (current(request.signal)) {
			phase = 'unknown';
			retryReady = false;
			error = cause instanceof SecretApiError ? cause.message : 'Submission was not confirmed.';
			void focus();
		}
	}
}

function validSize(): boolean {
	const size = mode === 'text' ? new TextEncoder().encode(message).length : selected?.size;
	const max = mode === 'text' ? limits.maxTextBytes : limits.maxFileBytes;
	if (size === undefined || size === 0 || size > max) {
		error = `Choose content between 1 and ${max.toLocaleString()} bytes.`;
		return false;
	}
	return true;
}
async function encryptSelection(key: CryptoKey) {
	const content =
		mode === 'text'
			? { kind: 'text' as const, text: message }
			: {
					kind: 'file' as const,
					bytes: new Uint8Array(await (selected as File).arrayBuffer()),
					filename: (selected as File).name
				};
	return encryptRequestPayload(requestId, key, content, limits);
}
function encryptionFailed(): void {
	if (!alive || hasExpired()) {
		return;
	}
	phase = 'ready';
	error =
		'Could not encrypt the content. Check the file name and size, and use a browser with Web Crypto over HTTPS or localhost.';
}
async function submit(): Promise<void> {
	if (busy || phase !== 'ready' || !publicKey || !instructions) {
		return;
	}
	clock();
	if (phase !== 'ready') {
		return;
	}
	if (!validSize()) {
		return;
	}
	busy = true;
	phase = 'submitting';
	error = '';
	notice = '';
	progress = null;
	const request = new AbortController();
	controller = request;
	try {
		const payload = await encryptSelection(publicKey);
		if (!current(request.signal) || hasExpired()) {
			return;
		}
		pending =
			payload.kind === 'file' && payload.size_bytes > limits.maxTextBytes
				? await prepareLargeSubmission(payload, instructions.generation)
				: prepareSubmission(payload, instructions.generation);
		if (!current(request.signal)) {
			pending = null;
			return;
		}
		clearInput();
		await sendPending(request);
	} catch {
		encryptionFailed();
	} finally {
		if (alive && controller === request) {
			busy = false;
		}
	}
}

async function applyOutcome(outcome: AttemptOutcome, signal: AbortSignal): Promise<void> {
	if (!current(signal)) {
		return;
	}
	clock();
	if (hasExpired()) {
		return;
	}
	if (outcome.state === 'accepted') {
		if (cancelRequested) {
			notice =
				'The server accepted the submission before cancellation. Only the requester can cancel accepted content.';
		}
		accepted();
		return;
	}
	if (outcome.state === 'renewed') {
		clear();
		instructions = outcome.instructions;
		notice = 'The previous upload attempt ended. Choose a file to start a new attempt.';
		await pinKey(outcome.instructions, signal);
		return;
	}
	if (outcome.state === 'unavailable') {
		phase = 'unavailable';
		clear();
		return;
	}
	retryReady =
		!cancelRequested && !reservationExpired && (!reservation || !uploadExpired(reservation));
	if (!retryReady) {
		notice = cancelRequested
			? 'Cancellation is not confirmed. Retry cancellation with the same attempt.'
			: 'The upload instruction expired. Cancel this attempt before choosing another file.';
	}
}
async function checkAttempt(): Promise<void> {
	if (busy || !pending || phase !== 'unknown') {
		return;
	}
	busy = true;
	error = '';
	notice = '';
	retryReady = false;
	const request = new AbortController();
	controller = request;
	try {
		await applyOutcome(
			await inspectRequestAttempt(api, requestId, token, pending, request.signal),
			request.signal
		);
	} catch (cause) {
		if (current(request.signal)) {
			reportAttempt(cause);
		}
	} finally {
		if (alive && controller === request) {
			busy = false;
		}
	}
}
function reportAttempt(cause: unknown): void {
	if (cause instanceof RequestApiError && cause.status === 404) {
		phase = 'unavailable';
		clear();
	}
	error = cause instanceof SecretApiError ? cause.message : 'Could not verify the submission.';
}
async function retry(): Promise<void> {
	if (busy || !retryReady || !pending || phase !== 'unknown' || cancelRequested) {
		return;
	}
	clock();
	if (phase !== 'unknown' || reservationExpired) {
		return;
	}
	busy = true;
	retryReady = false;
	phase = 'submitting';
	error = '';
	notice = '';
	const request = new AbortController();
	controller = request;
	await sendPending(request);
	if (alive && controller === request) {
		busy = false;
	}
}
async function cancelUpload(): Promise<void> {
	if (!pending?.ciphertext || !instructions || (cancelRequested && busy)) {
		return;
	}
	controller?.abort();
	const request = new AbortController();
	controller = request;
	busy = true;
	cancelRequested = true;
	retryReady = false;
	phase = 'unknown';
	error = '';
	notice = '';
	try {
		await applyOutcome(
			await cancelRequestUpload(
				api,
				requestId,
				token,
				pending,
				instructions.expires_at,
				request.signal
			),
			request.signal
		);
	} catch (cause) {
		if (current(request.signal)) {
			reportAttempt(cause);
			notice =
				'Cancellation was not confirmed. Keep this tab open and check the attempt. Stopping a transfer does not delete uploaded bytes.';
		}
	} finally {
		if (alive && controller === request) {
			busy = false;
		}
	}
}
function progressText(value: FileUploadProgress | null): string {
	if (cancelRequested && busy) {
		return 'Cancelling upload…';
	}
	if (value?.stage === 'preparing') {
		return 'Reserving encrypted upload…';
	}
	if (value?.stage === 'finalizing') {
		return 'Verifying upload… Acceptance is not confirmed yet.';
	}
	if (value?.stage === 'uploading' && value.loaded !== undefined && value.total !== undefined) {
		return `Uploading encrypted file: ${value.loaded.toLocaleString()} of ${value.total.toLocaleString()} bytes (${Math.floor((value.loaded / value.total) * 100)}%). Acceptance requires verification.`;
	}
	return value?.stage === 'uploading'
		? 'Uploading encrypted file…'
		: 'Submitting encrypted content…';
}

onMount(() => {
	alive = true;
	resumed = performance
		.getEntriesByType('navigation')
		.some((entry) => (entry as PerformanceNavigationTiming).type === 'reload');
	if (!globalThis.crypto?.subtle) {
		phase = 'invalid';
		error = 'Request encryption requires a browser with Web Crypto over HTTPS or localhost.';
		return;
	}
	const link = submissionFromFragment(location.hash);
	if (link) {
		token = link.token;
		fingerprint = link.fingerprint;
		void load();
	} else {
		phase = 'invalid';
		error = 'Invalid submission link. Ask the requester for the complete link.';
	}
	void focus();
	const timer = window.setInterval(clock, 1000);
	const leave = () => {
		alive = false;
		controller?.abort();
		clear();
	};
	// BFCache keeps this cleared document alive; a reload must not replay its lost attempt.
	const resume = (event: PageTransitionEvent) => {
		if (event.persisted) {
			location.reload();
		}
	};
	window.addEventListener('pageshow', resume);
	window.addEventListener('pagehide', leave);
	return () => {
		leave();
		window.clearInterval(timer);
		window.removeEventListener('pagehide', leave);
		window.removeEventListener('pageshow', resume);
	};
});
</script>

<svelte:head><title>Submit a secret - Flick</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<DeliveryFrame>
	<div class="grid gap-2">
		<h1 class="font-serif text-4xl" tabindex="-1" bind:this={heading}>{title}</h1>
		<p class="text-sm text-muted-foreground">Your browser encrypts the content for the requester. The first accepted submission wins.</p>
		{#if instructions && !terminal}<p class="text-sm" aria-label="Time remaining">{Math.floor(remaining / 60)}m {remaining % 60}s remaining</p>{/if}
	</div>
	{#if resumed}<p role="status" class="text-sm text-muted-foreground">The previous in-memory attempt was discarded when you left or reloaded. Its submission may already have been accepted. This page cannot recover or retry that attempt.</p>{/if}
	{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
	{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
	{#if phase === 'accepted'}
		<p role="status">The server confirmed acceptance. This does not confirm that the requester retrieved or read it.</p>
	{:else if hasExpired()}
		<p role="status">The deadline passed. Ask the requester to create a new request.</p>
	{:else if phase === 'unavailable'}
		<p role="status">This link is no longer accepting submissions. It may already have a submission, have been cancelled, or have expired.</p>
	{:else if phase === 'unknown' || (phase === 'submitting' && pending)}
		<section class="grid gap-4 rounded-xl border p-5" aria-label="Submission outcome">
			<p role="status">{phase === 'submitting' || (cancelRequested && busy) ? progressText(progress) : 'Submission outcome unknown.'}</p>
			<p class="text-sm text-muted-foreground">Keep this tab open. Check the attempt before retrying. A retry sends exactly the same encrypted content. Reloading or leaving discards this attempt; Flick cannot recover it for you.</p>
			{#if reservation}<p class="text-sm">Upload reservation: {Math.floor(remainingSecondsFrom(reservation.reservation_expires_at, now) / 60)}m {remainingSecondsFrom(reservation.reservation_expires_at, now) % 60}s remaining.</p>{/if}
			<Button variant="outline" disabled={busy} onclick={() => { void checkAttempt(); }}>Check submission</Button>
			{#if pending?.ciphertext}<Button variant="destructive" disabled={cancelRequested && busy} onclick={() => { void cancelUpload(); }}>{cancelRequested ? 'Retry cancellation' : 'Cancel upload'}</Button><p class="text-sm text-muted-foreground">Cancellation must be confirmed by the server before another attempt. Stopping the transfer does not delete uploaded bytes; server cleanup handles them.</p>{/if}
			{#if retryReady}<p role="status">The server has not accepted this attempt. You can retry the same submission.</p><Button disabled={busy} onclick={() => { void retry(); }}>Retry same submission</Button>{/if}
		</section>
	{:else if phase === 'ready' || phase === 'submitting'}
		<form class="grid gap-5" onsubmit={(event) => { event.preventDefault(); void submit(); }}>
			<fieldset class="flex gap-5" disabled={busy}>
				<legend class="mb-2 text-sm font-medium">Content type</legend>
				<label class="flex items-center gap-2"><input type="radio" bind:group={mode} value="text" onchange={clearInput} />Text</label>
				<label class="flex items-center gap-2"><input type="radio" bind:group={mode} value="file" onchange={clearInput} />File</label>
			</fieldset>
			{#if mode === 'text'}
				<div class="grid gap-2"><Label for="request-message">Message</Label><Textarea id="request-message" rows={7} bind:value={message} disabled={busy} required autocomplete="off" /></div>
			{:else}
				<div class="grid gap-2"><Label for="request-file">File</Label><Input id="request-file" type="file" bind:ref={fileInput} disabled={busy} onchange={(event) => { selected = event.currentTarget.files?.[0] ?? null; }} required /></div>
			{/if}
			<p class="text-sm text-muted-foreground">{(mode === 'text' ? limits.maxTextBytes : limits.maxFileBytes).toLocaleString()} bytes maximum. One file per request. File names are encrypted too. {limits.maxFileBytes > limits.maxTextBytes ? 'Larger files use an encrypted upload and require server verification.' : 'Large-file storage is unavailable; only inline files are supported.'}</p>
			<Button type="submit" class="h-11" disabled={busy}>{busy ? 'Encrypting…' : 'Encrypt and submit'}</Button>
		</form>
	{:else if phase === 'loading'}
		<p role="status">{busy ? 'Loading request…' : 'Request status is unknown.'}</p>
		<Button variant="outline" disabled={busy} onclick={() => { void load(); }}>Retry loading request</Button>
	{/if}
</DeliveryFrame>
