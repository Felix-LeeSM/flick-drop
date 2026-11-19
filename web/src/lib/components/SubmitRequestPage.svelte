<script lang="ts">
import { MessageSquareTextIcon, ShieldCheckIcon } from '@lucide/svelte';
import { onMount, tick as render } from 'svelte';
import { defaultLimits, getVerifiedConfig } from '#lib/api/config.js';
import {
	createRequestApiClient,
	prepareLargeSubmission,
	prepareSubmission,
	RequestApiError,
	type RequestInstructions,
	type ReservedUpload,
	requestLimits,
	type Submission
} from '#lib/api/requests.js';
import { DEFAULT_API_BASE_URL, type FileUploadProgress, SecretApiError } from '#lib/api/secrets.js';
import CredentialForm from '#lib/components/CredentialForm.svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import RemainingTime from '#lib/components/RemainingTime.svelte';
import SecretTypeSelector from '#lib/components/SecretTypeSelector.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Input } from '#lib/components/ui/input/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import { Textarea } from '#lib/components/ui/textarea/index.js';
import {
	buildEnvelope,
	type CredentialEnvelope,
	type CredentialType,
	credentialTemplateText,
	hasCredentialContent,
	isCredentialType,
	serializeCredential
} from '#lib/credentials/index.js';
import {
	encryptRequestPayload,
	importRequestPublicKey,
	openRequestNote
} from '#lib/crypto/requests.js';
import { t } from '#lib/i18n/index.js';
import { getLocale } from '#lib/i18n/locale.svelte.js';
import { remainingSecondsFrom } from '#lib/lifetime.js';
import { submissionFromFragment } from '#lib/state/request-links.js';
import {
	type AttemptOutcome,
	cancelRequestUpload,
	finishRequestUpload,
	inspectRequestAttempt,
	uploadExpired
} from '#lib/state/request-upload.js';
import { formatBytes } from '#lib/utils.js';

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
let mode = $state<'text' | 'file' | CredentialType>('text');
let message = $state('');
let credential = $state<CredentialEnvelope>(buildEnvelope('login'));
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
let memoKey = '';
let requesterNote = $state('');
let publicKey: string | null = null;
let pending = $state<Submission | null>(null);
let controller: AbortController | null = null;
let alive = false;
let configured = false;
const remaining = $derived(remainingSecondsFrom(instructions?.expires_at ?? '', now));
const terminal = $derived(['accepted', 'unavailable', 'expired', 'invalid'].includes(phase));
const title = $derived(
	phase === 'accepted'
		? t().request.submit.headingAccepted
		: hasExpired()
			? t().request.submit.headingExpired
			: phase === 'unavailable'
				? t().request.submit.headingUnavailable
				: t().request.submit.headingReady
);

function clearInput(): void {
	message = '';
	selected = null;
	if (fileInput) {
		fileInput.value = '';
	}
	// Reset even in Text or File mode, so credentials typed before switching do not stay in memory.
	credential = buildEnvelope(isCredentialType(mode) ? mode : 'login');
}
function switchMode(next: typeof mode): void {
	if (next !== mode) {
		mode = next;
		error = '';
		clearInput();
	}
}
// Shared with the type tablist: each tab's aria-controls and its panel's
// role="tabpanel" aria-labelledby both derive from this id.
function typePanelId(type: typeof mode): string {
	return `request-type-${type}`;
}
// Credential templates travel as FLCR1 text, exactly like credential secrets on the create page.
function textContent(): string {
	return mode === 'text' ? message : serializeCredential(credential);
}
function clear(keepMemo = false): void {
	clearInput();
	pending = null;
	publicKey = null;
	if (!keepMemo) {
		memoKey = '';
	}
	requesterNote = '';
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
			error = t().request.submit.errors.reservationExpired;
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
			error = t().request.submit.errors.keyMismatch;
		}
	}
}

function reportLoad(cause: unknown): void {
	if (cause instanceof RequestApiError && cause.status === 404) {
		phase = 'unavailable';
	}
	error = cause instanceof RequestApiError ? cause.message : t().request.submit.errors.loadFailed;
}
async function decryptNote(
	encryptedNote: string | undefined,
	signal: AbortSignal
): Promise<string | null> {
	if ((encryptedNote !== undefined) !== (memoKey !== '')) {
		phase = 'invalid';
		error = t().request.submit.errors.noteMismatch;
		return null;
	}
	if (!encryptedNote || !memoKey) {
		return '';
	}
	try {
		const decrypted = await openRequestNote(memoKey, encryptedNote);
		return current(signal) ? decrypted : null;
	} catch {
		if (current(signal)) {
			phase = 'invalid';
			error = t().request.submit.errors.noteDecryptFailed;
		}
		return null;
	}
}

async function unlockInstructions(data: RequestInstructions, signal: AbortSignal): Promise<void> {
	const decryptedNote = await decryptNote(data.encrypted_note, signal);
	if (decryptedNote === null) {
		return;
	}
	await pinKey(data, signal);
	if (current(signal) && phase === 'ready') {
		requesterNote = decryptedNote;
	}
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
		await unlockInstructions(data, request.signal);
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
		if (pending.upload === undefined) {
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
			error =
				cause instanceof SecretApiError
					? cause.message
					: t().request.submit.errors.submitUnconfirmed;
			void focus();
		}
	}
}

function validSize(): boolean {
	if (isCredentialType(mode) && !hasCredentialContent(credential)) {
		error = t().request.submit.errors.fillField;
		return false;
	}
	const size = mode === 'file' ? selected?.size : new TextEncoder().encode(textContent()).length;
	const max = mode === 'file' ? limits.maxFileBytes : limits.maxTextBytes;
	if (size === undefined || size === 0 || size > max) {
		error = t().request.submit.errors.sizeRange(max.toLocaleString(getLocale()));
		return false;
	}
	return true;
}
function encryptSelection(recipient: string) {
	const content =
		mode === 'file'
			? { kind: 'file' as const, file: selected as File }
			: { kind: 'text' as const, text: textContent() };
	return encryptRequestPayload(recipient, content, limits);
}
function encryptionFailed(): void {
	if (!alive || hasExpired()) {
		return;
	}
	phase = 'ready';
	error = t().request.submit.errors.encryptFailed;
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
		// Route on the plaintext size, as /api/config limits are; the API also
		// accepts a large upload of any size, so neither path strands a file.
		pending =
			payload.kind === 'file' && payload.plaintextBytes > limits.maxTextBytes
				? await prepareLargeSubmission(payload, instructions.generation)
				: await prepareSubmission(payload, instructions.generation);
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
			notice = t().request.submit.notices.acceptedBeforeCancel;
		}
		accepted();
		return;
	}
	if (outcome.state === 'renewed') {
		clear(true);
		instructions = outcome.instructions;
		notice = t().request.submit.notices.attemptRenewed;
		await unlockInstructions(outcome.instructions, signal);
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
			? t().request.submit.notices.cancelNotConfirmed
			: t().request.submit.notices.instructionExpired;
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
	error = cause instanceof SecretApiError ? cause.message : t().request.submit.errors.verifyFailed;
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
	if (!pending?.upload || !instructions || (cancelRequested && busy)) {
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
			notice = t().request.submit.notices.cancelUnconfirmed;
		}
	} finally {
		if (alive && controller === request) {
			busy = false;
		}
	}
}
function progressText(value: FileUploadProgress | null): string {
	const text = t().request.submit.progress;
	if (cancelRequested && busy) {
		return text.cancelling;
	}
	if (value?.stage === 'preparing') {
		return text.reserving;
	}
	if (value?.stage === 'finalizing') {
		return text.verifying;
	}
	if (value?.stage === 'uploading' && value.loaded !== undefined && value.total !== undefined) {
		return text.uploadingBytes(
			value.loaded.toLocaleString(getLocale()),
			value.total.toLocaleString(getLocale()),
			Math.floor((value.loaded / value.total) * 100)
		);
	}
	return value?.stage === 'uploading' ? text.uploading : text.submitting;
}

onMount(() => {
	alive = true;
	resumed = performance
		.getEntriesByType('navigation')
		.some((entry) => (entry as PerformanceNavigationTiming).type === 'reload');
	if (!globalThis.crypto?.subtle) {
		phase = 'invalid';
		error = t().request.submit.errors.noWebCrypto;
		return;
	}
	const link = submissionFromFragment(location.hash);
	if (link) {
		token = link.token;
		fingerprint = link.fingerprint;
		memoKey = link.memoKey ?? '';
		void load();
	} else {
		phase = 'invalid';
		error = t().request.submit.errors.invalidLink;
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

<svelte:head><title>{t().request.submit.pageTitle}</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<DeliveryFrame>
	<div class="grid gap-1.5">
		<p class="micro flex items-center gap-1.5 text-muted-foreground">
			<ShieldCheckIcon class="size-3.5 text-primary" aria-hidden="true" />
			{t().request.shared.badge}
		</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{title}</h1>
		{#if !terminal}<p class="text-sm text-muted-foreground">{t().request.submit.intro}</p>{/if}
		{#if instructions && !terminal}<RemainingTime seconds={remaining} aria-label={t().request.shared.timeRemaining} class="mt-1" />{/if}
	</div>
	{#if resumed}<p role="status" class="text-sm text-muted-foreground">{t().request.submit.resumed}</p>{/if}
	{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
	{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
	{#if phase === 'accepted'}
		<p role="status">{t().request.submit.acceptedBody}</p>
	{:else if hasExpired()}
		<p role="status">{t().request.submit.expiredBody}</p>
	{:else if phase === 'unavailable'}
		<p role="status">{t().request.submit.unavailableBody}</p>
	{:else if phase === 'unknown' || (phase === 'submitting' && pending)}
		<section class="grid gap-4 rounded-[14px] border bg-card px-5 py-4.5" aria-label={t().request.submit.outcomeRegion}>
			<p role="status">{phase === 'submitting' || (cancelRequested && busy) ? progressText(progress) : t().request.submit.outcomeUnknown}</p>
			<p class="text-sm text-muted-foreground">{t().request.submit.outcomeHint}</p>
			{#if reservation}<p class="text-sm">{t().request.submit.reservationRemaining(Math.floor(remainingSecondsFrom(reservation.reservation_expires_at, now) / 60), remainingSecondsFrom(reservation.reservation_expires_at, now) % 60)}</p>{/if}
			<Button variant="outline" disabled={busy} onclick={() => { void checkAttempt(); }}>{t().request.submit.checkSubmission}</Button>
			{#if pending?.upload}<Button variant="destructive" disabled={cancelRequested && busy} onclick={() => { void cancelUpload(); }}>{cancelRequested ? t().request.submit.retryCancellation : t().request.submit.cancelUpload}</Button><p class="text-sm text-muted-foreground">{t().request.submit.cancelHint}</p>{/if}
			{#if retryReady}<p role="status">{t().request.submit.retryReady}</p><Button disabled={busy} onclick={() => { void retry(); }}>{t().request.submit.retrySame}</Button>{/if}
		</section>
	{:else if phase === 'ready' || phase === 'submitting'}
		{#if requesterNote.length > 0}
			<section
				class="grid min-w-0 gap-1 rounded-xl border border-border bg-muted/30 px-4 py-3 text-left"
				aria-label={t().request.submit.requesterNote}
			>
				<p class="micro flex items-center gap-1.5 text-muted-foreground">
					<MessageSquareTextIcon class="size-3.5 shrink-0 text-primary" aria-hidden="true" />
					{t().request.submit.requesterNote}
				</p>
				<p class="text-sm whitespace-pre-wrap [overflow-wrap:anywhere] text-foreground">
					{requesterNote}
				</p>
			</section>
		{/if}
		<form class="grid gap-6" onsubmit={(event) => { event.preventDefault(); void submit(); }}>
			<SecretTypeSelector value={mode} disabled={busy} panelId={typePanelId} onSelect={switchMode} />
			{#if mode === 'text'}
				<div class="grid gap-2" id={typePanelId(mode)} role="tabpanel" aria-labelledby={`${typePanelId(mode)}-tab`}><Label for="request-message">{t().request.submit.messageLabel}</Label><Textarea id="request-message" rows={7} class="min-h-44 resize-y px-3.5 py-3" bind:value={message} disabled={busy} required autocomplete="off" /></div>
			{:else if mode === 'file'}
				<div class="grid gap-2" id={typePanelId(mode)} role="tabpanel" aria-labelledby={`${typePanelId(mode)}-tab`}><Label for="request-file">{t().request.submit.fileLabel}</Label><Input id="request-file" type="file" bind:ref={fileInput} disabled={busy} onchange={(event) => { selected = event.currentTarget.files?.[0] ?? null; }} required /></div>
			{:else}
				<div class="grid gap-2.5" id={typePanelId(mode)} role="tabpanel" aria-labelledby={`${typePanelId(mode)}-tab`}><span class="text-sm font-medium">{credentialTemplateText(mode).name}</span><CredentialForm bind:envelope={credential} disabled={busy} /></div>
			{/if}
			{#if mode === 'file'}<p class="text-sm text-muted-foreground">{t().request.submit.fileLimit(formatBytes(limits.maxFileBytes))}</p>{/if}
			<Button type="submit" size="cta" disabled={busy}>{busy ? t().request.submit.encrypting : t().request.submit.submit}</Button>
		</form>
	{:else if phase === 'loading'}
		<p role="status">{busy ? t().request.submit.loading : t().request.shared.statusUnknown}</p>
		<Button variant="outline" disabled={busy} onclick={() => { void load(); }}>{t().request.submit.retryLoading}</Button>
	{/if}
</DeliveryFrame>
