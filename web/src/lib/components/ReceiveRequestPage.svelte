<script lang="ts">
import { QrCodeIcon } from '@lucide/svelte';
import { onMount, tick as render } from 'svelte';
import { resolve } from '$app/paths';
import { defaultLimits, getVerifiedConfig } from '$lib/api/config';
import {
	createRequestApiClient,
	RequestApiError,
	type RequestOwner,
	requestLimits
} from '$lib/api/requests';
import { DEFAULT_API_BASE_URL } from '$lib/api/secrets';
import NativeShareButton from '$lib/components/NativeShareButton.svelte';
import QrModal from '$lib/components/QrModal.svelte';
import RequestFrame from '$lib/components/RequestFrame.svelte';
import UrlField from '$lib/components/UrlField.svelte';
import { Button } from '$lib/components/ui/button';
import { Textarea } from '$lib/components/ui/textarea';
import {
	decryptRequestPayload,
	importRequestPrivateKey,
	type RequestContent
} from '$lib/crypto/requests';
import { remainingSecondsFrom } from '$lib/lifetime';
import {
	clearRequestHandoff,
	retrievalFromFragment,
	retrievalUrl,
	takeRequest
} from '$lib/state/request-links';

let { requestId }: { requestId: string } = $props();
const api = createRequestApiClient();
let snapshot = $state<RequestOwner | null>(null);
let submission = $state('');
let privateLink = $state('');
let privateKey = $state<CryptoKey | null>(null);
let keyProblem = $state('');
let busy = $state(false);
let error = $state('');
let notice = $state('');
let expired = $state(false);
let unavailable = $state(false);
let opened = $state(false);
let openLost = $state(false);
let decryptFailed = $state(false);
let content = $state<RequestContent | null>(null);
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
		? 'Request expired'
		: unavailable
			? 'Request unavailable'
			: content
				? 'Opened once'
				: openLost
					? 'Open outcome unknown'
					: decryptFailed
						? 'Could not decrypt'
						: snapshot?.state === 'submitted'
							? 'Ready to retrieve'
							: snapshot?.state === 'consumed'
								? 'Consumed'
								: snapshot?.state === 'cancelled'
									? 'Cancelled'
									: snapshot?.state === 'waiting'
										? 'Waiting for a submission'
										: 'Request status'
);

function clearContent(): void {
	if (content?.kind === 'file') {
		content.bytes.fill(0);
	}
	content = null;
	if (downloadUrl) {
		URL.revokeObjectURL(downloadUrl);
		downloadUrl = '';
	}
}
function clearLinks(): void {
	privateKey = null;
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
		keyProblem =
			'Missing private key. Reopen the complete private retrieval link. Flick cannot recover a lost key or decrypt without it.';
		return;
	}
	if (privateKey && verifiedFingerprint === data.fingerprint) {
		return;
	}
	privateKey = null;
	try {
		const key = await importRequestPrivateKey(keyFragment, data.fingerprint);
		if (alive && !expired && !controller?.signal.aborted) {
			privateKey = key;
			verifiedFingerprint = data.fingerprint;
			keyProblem = '';
		}
	} catch {
		keyProblem =
			'Invalid private key. Reopen the original complete private retrieval link. Nothing was opened; Flick cannot recover a missing or damaged key.';
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
			cause instanceof RequestApiError
				? cause.message
				: 'Could not verify request status. Try again.';
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
			error =
				'Could not verify size limits. Refresh status before opening. Nothing has been opened.';
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
			await cancelFresh(request.signal);
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
async function cancelFresh(signal: AbortSignal): Promise<void> {
	if (
		!window.confirm(
			'Cancel this request permanently? It will stop accepting submissions and the requester will not be able to open any stored content. Already released copies cannot be recalled.'
		)
	) {
		return;
	}
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
				notice = 'The request changed before cancellation. The current server status is shown.';
				void focus();
			}
		} else {
			throw cause;
		}
	}
}
function current(signal: AbortSignal): boolean {
	return alive && !signal.aborted;
}
async function reveal(payload: unknown, key: CryptoKey, signal: AbortSignal): Promise<void> {
	if (!current(signal) || !snapshot) {
		return;
	}
	snapshot = { ...snapshot, state: 'consumed' };
	const decrypted = await decryptRequestPayload(requestId, key, payload, limits);
	clock();
	if (!current(signal) || expired) {
		if (decrypted.kind === 'file') {
			decrypted.bytes.fill(0);
		}
		return;
	}
	content = decrypted;
	if (decrypted.kind === 'file') {
		downloadUrl = URL.createObjectURL(
			new Blob([decrypted.bytes], { type: 'application/octet-stream' })
		);
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
		notice =
			'The server did not release content for this opening. The request may have changed in another tab.';
		return;
	}
	openLost = true;
	error = '';
}
async function open(): Promise<void> {
	if (busy || opened || terminal || snapshot?.state !== 'submitted' || !privateKey || !configured) {
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
	const key = privateKey;
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
		keyFragment = link.privateKey;
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

<svelte:head><title>Private retrieval - Flick</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<RequestFrame>
	<div class="grid gap-2">
		<h1 class="font-serif text-4xl" tabindex="-1" bind:this={heading}>{title}</h1>
		<p class="text-sm text-muted-foreground">Private requester page. Checking status does not open the content.</p>
	</div>
	<section class="grid gap-3 rounded-xl border p-5" aria-label="Request status">
		{#if expired}<p role="status">The deadline passed. The request and any content shown here are no longer available.</p>
		{:else if unavailable}<p role="status">The link is invalid or expired. Use the complete private retrieval link.</p>
		{:else if snapshot}
			<p role="status">{error ? 'Last known status: ' : ''}{snapshot.state === 'waiting' ? 'Waiting for the first submission.' : snapshot.state === 'submitted' ? 'A submission is ready. Open releases it once.' : snapshot.state === 'cancelled' ? 'This request was cancelled.' : 'The server released the encrypted content once. This does not confirm that anyone read it.'}</p>
			<p class="text-sm" aria-label="Time remaining">{Math.floor(remaining / 60)}m {remaining % 60}s remaining</p>
		{:else}<p role="status">{busy ? 'Loading request status…' : 'Request status is unknown.'}</p>{/if}
		{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
		{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
		{#if !terminal}<Button variant="outline" disabled={busy} onclick={() => { void refresh(); }}>Refresh status</Button>{/if}
	</section>
	{#if keyProblem && !terminal}<p role="alert" class="text-sm text-destructive">{keyProblem}</p>{/if}
	{#if openLost && !expired}<p role="alert" class="text-sm text-destructive">The open response was lost or invalid. The server may already have consumed the content. Flick cannot recover a lost open response. This page will not retry opening.</p>{/if}
	{#if decryptFailed}<p role="alert" class="text-sm text-destructive">The content was released, but decryption failed. It cannot be opened again. Ask the sender to use a new request.</p>{/if}
	{#if content?.kind === 'text'}
		<Textarea aria-label="Decrypted secret" value={content.text} readonly rows={8} />
	{:else if content?.kind === 'file'}
		<a href={downloadUrl} download={content.filename} class="break-all rounded-lg bg-primary px-4 py-3 text-center text-sm text-primary-foreground">Download {content.filename}</a>
	{/if}
	{#if content}<p class="text-sm text-muted-foreground">Save it now. Leaving, refreshing, or reaching the deadline clears this copy; the server cannot release it again.</p>{/if}
	{#if snapshot?.state === 'submitted' && !terminal && !opened}
		<Button class="h-11" disabled={busy || !privateKey} onclick={() => { void open(); }}>{busy ? 'Working…' : 'Open once'}</Button>
	{/if}
	{#if submission && !terminal}
		<section class="grid gap-3" aria-labelledby="submission-link-heading">
			<h2 id="submission-link-heading" class="font-medium">Share the submission link</h2>
			<p class="text-sm text-muted-foreground">Anyone with this link can submit once. It cannot retrieve, decrypt, or cancel the request.</p>
			<UrlField value={submission} label="Submission link" copyLabel="Copy submission link" />
			<NativeShareButton recipientUrl={submission} />
			<Button variant="outline" onclick={() => { qrOpen = true; }}><QrCodeIcon class="size-4" aria-hidden="true" />Show submission QR</Button>
		</section>
	{:else if !terminal}<p class="text-sm text-muted-foreground">The submission link is available only in the original creation session. Use the copy you saved; this private link cannot reconstruct it.</p>{/if}
	{#if privateLink && !terminal}
		<section class="grid gap-3 rounded-xl bg-muted/40 p-5" aria-labelledby="retrieval-link-heading">
			<h2 id="retrieval-link-heading" class="font-medium">Keep your retrieval link private</h2>
			<p class="text-sm text-muted-foreground">This link contains your decryption key. Anyone with it can retrieve or cancel the request. Save the complete link to resume here or on another device. Losing the key is irreversible; Flick cannot recover it.</p>
			<p class="text-sm text-muted-foreground">Browser history and sync, clipboard managers, and screenshots may retain the key. Share only the separate submission link.</p>
			<UrlField value={privateLink} label="Private retrieval link" copyLabel="Copy private retrieval link" />
		</section>
	{/if}
	{#if snapshot && !terminal}<Button variant="destructive" disabled={busy} onclick={() => { void refresh(true); }}>Cancel request</Button>{/if}
	<a href={resolve('/request')} class="py-3 text-center text-sm underline underline-offset-4">Create another request</a>
</RequestFrame>
<QrModal bind:open={qrOpen} url={submission} />
