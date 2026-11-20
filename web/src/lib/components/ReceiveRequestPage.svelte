<script lang="ts">
import { QrCodeIcon } from '@lucide/svelte';
import { onMount, tick as render } from 'svelte';
import { defaultLimits, getVerifiedConfig } from '#lib/api/config.js';
import {
	createRequestApiClient,
	RequestApiError,
	type RequestOwner,
	requestLimits
} from '#lib/api/requests.js';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
import CredentialView from '#lib/components/CredentialView.svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import NativeShareButton from '#lib/components/NativeShareButton.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import RemainingTime from '#lib/components/RemainingTime.svelte';
import UrlField from '#lib/components/UrlField.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Textarea } from '#lib/components/ui/textarea/index.js';
import { parseCredential } from '#lib/credentials/index.js';
import {
	decryptRequestPayload,
	importRequestIdentity,
	type OpenedRequestBody,
	type OpenedRequestContent
} from '#lib/crypto/requests.js';
import { t } from '#lib/i18n/index.js';
import { remainingSecondsFrom } from '#lib/lifetime.js';
import {
	clearRequestHandoff,
	retrievalFromFragment,
	retrievalUrl,
	takeRequest
} from '#lib/state/request-links.js';
import { resolve } from '$app/paths';

let { requestId }: { requestId: string } = $props();
const api = createRequestApiClient();
let snapshot = $state<RequestOwner | null>(null);
let submission = $state('');
let privateLink = $state('');
let cancelConfirmOpen = $state(false);
let pendingSignal: AbortSignal | null = null;
let identity = $state<string | null>(null);
let keyProblem = $state('');
let busy = $state(false);
let error = $state('');
let notice = $state('');
let expired = $state(false);
let unavailable = $state(false);
let opened = $state(false);
let openLost = $state(false);
let decryptFailed = $state(false);
let content = $state<OpenedRequestContent | null>(null);
const credential = $derived(content?.kind === 'text' ? parseCredential(content.text) : null);
let downloadUrl = $state('');
let qrOpen = $state(false);
let heading = $state<HTMLHeadingElement | null>(null);
let now = $state(Date.now());
let token = '';
let keyFragment = '';
let verifiedFingerprint = '';
let limits = requestLimits(defaultLimits());
let controller: AbortController | null = null;
let alive = false;
let configured = false;
let lastRefresh = 0;
let pollDelay = 10_000;
const terminal = $derived(
	snapshot?.state === 'consumed' || snapshot?.state === 'cancelled' || expired || unavailable
);
const remaining = $derived(remainingSecondsFrom(snapshot?.expires_at ?? '', now));
const title = $derived(
	expired
		? t().request.receive.headingExpired
		: unavailable
			? t().request.receive.headingUnavailable
			: content
				? t().request.receive.headingOpened
				: openLost
					? t().request.receive.headingOpenLost
					: decryptFailed
						? t().request.receive.headingDecryptFailed
						: snapshot?.state === 'submitted'
							? t().request.receive.headingReady
							: snapshot?.state === 'consumed'
								? t().request.receive.headingConsumed
								: snapshot?.state === 'cancelled'
									? t().request.receive.headingCancelled
									: snapshot?.state === 'waiting' || snapshot?.state === 'uploading'
										? t().request.receive.headingWaiting
										: t().request.receive.headingStatus
);

function clearContent(): void {
	content = null;
	if (downloadUrl) {
		URL.revokeObjectURL(downloadUrl);
		downloadUrl = '';
	}
}
function clearLinks(): void {
	identity = null;
	keyFragment = '';
	verifiedFingerprint = '';
	privateLink = '';
	submission = '';
	qrOpen = false;
	clearRequestHandoff();
}
function clock(): void {
	now = Date.now();
	if (snapshot && now >= Date.parse(snapshot.expires_at) && !expired) {
		expired = true;
		error = '';
		notice = '';
		controller?.abort();
		clearContent();
		clearLinks();
	}
}
async function focus(): Promise<void> {
	await render();
	if (alive) {
		heading?.focus();
	}
}
async function accept(data: RequestOwner): Promise<void> {
	snapshot = data;
	error = '';
	pollDelay = 10_000;
	clock();
	if (terminal) {
		clearLinks();
		return;
	}
	if (!keyFragment) {
		keyProblem = t().request.receive.errors.missingKey;
		return;
	}
	if (identity && verifiedFingerprint === data.fingerprint) {
		return;
	}
	identity = null;
	try {
		const key = await importRequestIdentity(keyFragment, data.fingerprint);
		if (alive && !expired && !controller?.signal.aborted) {
			identity = key;
			verifiedFingerprint = data.fingerprint;
			keyProblem = '';
		}
	} catch {
		keyProblem = t().request.receive.errors.invalidKey;
	}
}
function report(cause: unknown): void {
	pollDelay = Math.min(pollDelay * 2, 120_000);
	if (cause instanceof RequestApiError && cause.status === 404) {
		unavailable = true;
		error = '';
		clearContent();
		clearLinks();
	} else {
		error =
			cause instanceof RequestApiError ? cause.message : t().request.receive.errors.statusFailed;
	}
}
async function readOwner(signal: AbortSignal): Promise<void> {
	lastRefresh = Date.now();
	const data = await api.owner(requestId, token, signal);
	if (alive && !signal.aborted) {
		await accept(data);
	}
}
async function verifyLimits(signal: AbortSignal): Promise<boolean> {
	if (configured) {
		return true;
	}
	try {
		limits = requestLimits(await getVerifiedConfig(DEFAULT_API_BASE_URL, fetch, signal));
		if (!current(signal)) {
			return false;
		}
		configured = true;
		return true;
	} catch {
		if (current(signal)) {
			error = t().request.receive.errors.limitsFailed;
			pollDelay = Math.min(pollDelay * 2, 120_000);
			lastRefresh = Date.now();
		}
		return false;
	}
}
async function refresh(cancel = false): Promise<void> {
	if (busy || !token || terminal) {
		return;
	}
	busy = true;
	notice = '';
	const request = new AbortController();
	controller = request;
	try {
		if (!(await verifyLimits(request.signal))) {
			return;
		}
		await readOwner(request.signal);
		if (cancel && alive && !request.signal.aborted && !terminal) {
			queueCancelFresh(request.signal);
		}
	} catch (cause) {
		if (alive && !request.signal.aborted) {
			report(cause);
		}
	} finally {
		if (alive) {
			busy = false;
		}
	}
}
function queueCancelFresh(signal: AbortSignal): void {
	pendingSignal = signal;
	cancelConfirmOpen = true;
}

async function cancelPendingRequest(): Promise<void> {
	const signal = pendingSignal;
	pendingSignal = null;
	if (!signal || signal.aborted) {
		return;
	}
	// Re-entry guard: keep buttons disabled for the whole revoke.
	busy = true;
	try {
		await api.revoke(requestId, token, signal);
		if (alive && !signal.aborted && snapshot) {
			await accept({ ...snapshot, state: 'cancelled' });
			void focus();
		}
	} catch (cause) {
		if (cause instanceof RequestApiError && cause.status === 409) {
			await readOwner(signal);
			if (alive && !signal.aborted) {
				notice = t().request.receive.notices.changedBeforeCancel;
				void focus();
			}
		} else if (!signal.aborted) {
			report(cause);
		}
	} finally {
		busy = false;
	}
}
function current(signal: AbortSignal): boolean {
	return alive && !signal.aborted;
}
async function reveal(payload: OpenedRequestBody, key: string, signal: AbortSignal): Promise<void> {
	if (!current(signal) || !snapshot) {
		return;
	}
	snapshot = { ...snapshot, state: 'consumed' };
	const decrypted = await decryptRequestPayload(key, payload, limits);
	clock();
	if (!current(signal) || expired) {
		return;
	}
	content = decrypted;
	if (decrypted.kind === 'file') {
		downloadUrl = URL.createObjectURL(decrypted.blob);
	}
	clearLinks();
}
async function openFailure(cause: unknown, released: boolean, signal: AbortSignal): Promise<void> {
	if (!current(signal)) {
		return;
	}
	if (released) {
		decryptFailed = true;
		clearLinks();
		return;
	}
	if (cause instanceof RequestApiError && [429, 503].includes(cause.status)) {
		opened = false;
		report(cause);
		return;
	}
	if (cause instanceof RequestApiError && [404, 409].includes(cause.status)) {
		try {
			await readOwner(signal);
		} catch (statusError) {
			if (current(signal)) {
				report(statusError);
			}
		}
		notice = t().request.receive.notices.notReleased;
		return;
	}
	openLost = true;
	error = '';
}
async function open(): Promise<void> {
	if (busy || opened || terminal || snapshot?.state !== 'submitted' || !identity || !configured) {
		return;
	}
	clock();
	if (expired) {
		return;
	}
	busy = true;
	opened = true;
	error = '';
	notice = '';
	const key = identity;
	const request = new AbortController();
	controller = request;
	let released = false;
	try {
		const payload = await api.open(requestId, token, request.signal);
		released = true;
		await reveal(payload, key, request.signal);
	} catch (cause) {
		await openFailure(cause, released, request.signal);
	} finally {
		if (alive) {
			busy = false;
			void focus();
		}
	}
}

onMount(() => {
	alive = true;
	const link = retrievalFromFragment(location.hash);
	if (link) {
		token = link.token;
		keyFragment = link.identity;
		if (keyFragment) {
			privateLink = retrievalUrl(location.origin, requestId, token, keyFragment);
		}
		submission = takeRequest(requestId, token)?.submissionUrl ?? '';
		void refresh();
	} else {
		unavailable = true;
		clearRequestHandoff();
	}
	void focus();
	const poll = () => {
		clock();
		if (!document.hidden && Date.now() - lastRefresh >= pollDelay && !terminal && !opened) {
			void refresh();
		}
	};
	const timer = window.setInterval(poll, 1000);
	document.addEventListener('visibilitychange', poll);
	const leave = () => {
		alive = false;
		controller?.abort();
		clearContent();
		clearLinks();
		token = '';
	};
	// BFCache restores the same document without onMount; reload only metadata/key validation.
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
		document.removeEventListener('visibilitychange', poll);
		window.removeEventListener('pagehide', leave);
		window.removeEventListener('pageshow', resume);
	};
});
</script>

<svelte:head><title>{t().request.receive.pageTitle}</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<DeliveryFrame>
	<div class="grid gap-1.5">
		<p class="micro text-muted-foreground">{t().request.receive.badge}</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{title}</h1>
	</div>
	<section class="flex flex-wrap items-start justify-between gap-x-3 gap-y-2 rounded-[14px] border bg-card px-5 py-4.5" aria-label={t().request.receive.statusRegion}>
		<div class="grid grow basis-32 gap-1">
		{#if expired}<p role="status">{t().request.receive.expiredBody}</p>
		{:else if unavailable}<p role="status">{t().request.receive.unavailableBody}</p>
			{:else if snapshot}
				<!-- Visually hidden while waiting, but announced when polling changes it. -->

				<p
					role="status"
					class={snapshot.state === 'waiting' && !error ? 'sr-only' : undefined}
				>
					{error ? t().request.receive.lastKnown : ''}{snapshot.state === 'uploading'
						? t().request.receive.uploading
						: snapshot.state === 'waiting'
							? t().request.receive.waiting
							: snapshot.state === 'submitted'
								? openLost || keyProblem
									? t().request.receive.accepted
									: t().request.receive.openOnce
								: snapshot.state === 'cancelled'
									? t().request.receive.cancelled
									: content
										? t().request.receive.saveNow
										: t().request.receive.consumed}
				</p>

				{#if (snapshot.state === 'waiting' || snapshot.state === 'uploading') && !error}
					<p class="flex items-center gap-2.5 text-sm">
						<span class="relative flex size-2" aria-hidden="true">
						<span class="absolute inline-flex size-full rounded-full bg-primary opacity-60 motion-safe:animate-ping"></span>
						<span class="relative inline-flex size-2 rounded-full bg-primary"></span>
						</span>
						{t().request.receive.checking}
					</p>
				{/if}
			{#if !terminal || content}<RemainingTime seconds={remaining} aria-label={t().request.shared.timeRemaining} class="mt-1.5" />{/if}
		{:else}<p role="status">{busy ? t().request.receive.loadingStatus : t().request.shared.statusUnknown}</p>{/if}
		{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
		{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
		</div>
		{#if !terminal}<Button variant="ghost" size="sm" class="-mt-1 ml-auto text-[0.8125rem] text-muted-foreground" disabled={busy} onclick={() => { void refresh(); }}>{t().request.receive.refresh}</Button>{/if}
	</section>
	{#if keyProblem && !terminal}<p role="alert" class="text-sm text-destructive">{keyProblem}</p>{/if}
	{#if openLost && !expired}<p role="alert" class="text-sm text-destructive">{t().request.receive.openLost}</p>{/if}
	{#if decryptFailed}<p role="alert" class="text-sm text-destructive">{t().request.receive.decryptFailed}</p>{/if}
	{#if credential}
		<CredentialView credential={credential} />
	{:else if content?.kind === 'text'}
		<Textarea aria-label={t().request.receive.decryptedSecret} value={content.text} readonly rows={8} />
	{:else if content?.kind === 'file'}
		<a href={downloadUrl} download={content.filename} class="break-all rounded-lg bg-primary px-4 py-3 text-center text-sm text-primary-foreground">{t().request.receive.download(content.filename)}</a>
	{/if}
	{#if snapshot?.state === 'submitted' && !terminal && !opened}
		<Button size="cta" disabled={busy || !identity} onclick={() => { void open(); }}>{busy ? t().request.receive.working : t().request.receive.open}</Button>
	{/if}
	{#if submission && !terminal}
		<section class="grid gap-2.5" aria-labelledby="submission-link-heading">
			<h2 id="submission-link-heading" class="text-[0.9375rem] font-medium">{t().request.receive.shareHeading}</h2>
			<p class="text-[0.8125rem] text-muted-foreground">{t().request.receive.shareHint}</p>
			<UrlField value={submission} label={t().request.receive.submissionLink} copyLabel={t().request.receive.copySubmissionLink} prominent />
			<div class="flex flex-wrap items-start gap-2">
				<NativeShareButton recipientUrl={submission} class="h-9 w-auto justify-self-start rounded-lg px-3 text-[0.8125rem]" />
				<Button variant="outline" size="seg" onclick={() => { qrOpen = true; }}><QrCodeIcon class="size-4" aria-hidden="true" />{t().request.receive.showSubmissionQr}</Button>
			</div>
		</section>
	{:else if !terminal}<p class="text-sm text-muted-foreground">{t().request.receive.submissionGone}</p>{/if}
	{#if privateLink && !terminal}
		<section class="grid gap-2 rounded-[14px] border border-[color-mix(in_srgb,var(--burn)_28%,var(--border))] bg-[color-mix(in_srgb,var(--burn)_6%,var(--card))] px-5 py-4.5" aria-labelledby="retrieval-link-heading">
			<h2 id="retrieval-link-heading" class="text-[0.9375rem] font-medium">{t().request.receive.privateHeading}</h2>
			<p class="text-[0.8125rem] leading-[1.55] text-muted-foreground">{t().request.receive.privateBody}</p>
			<UrlField value={privateLink} label={t().request.receive.privateLink} copyLabel={t().request.receive.copyPrivateLink} />
		</section>
	{/if}

	{#if snapshot && !terminal}
		<Button
			variant="destructive"
			class="h-10 justify-self-center rounded-[10px] border-destructive/35 bg-transparent px-4.5 dark:bg-transparent"
			disabled={busy}
			onclick={() => {
				void refresh(true);
			}}
		>{t().request.receive.cancelRequest}</Button>
	{/if}

	<a
		href={resolve('request')}
		class="pb-3 text-center text-[0.8125rem] text-muted-foreground underline underline-offset-4"
	>{t().request.receive.createAnother}</a>
</DeliveryFrame>
<QrModal bind:open={qrOpen} url={submission} />
<ConfirmDialog
	bind:open={cancelConfirmOpen}
	title={t().request.receive.confirmTitle}
	description={t().request.receive.confirmBody}
	confirmLabel={t().request.receive.cancelRequest}
	onconfirm={() => { void cancelPendingRequest(); }}
/>
