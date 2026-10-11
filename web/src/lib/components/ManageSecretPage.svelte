<script lang="ts">
import { QrCodeIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { resolve } from '$app/paths';
import {
	createManagementApiClient,
	createManagementUrl,
	ManagementApiError,
	type ManagementSnapshot,
	type ManagementStatus,
	managementTokenFromFragment
} from '$lib/api/management';
import DeliveryFrame from '$lib/components/DeliveryFrame.svelte';
import NativeShareButton from '$lib/components/NativeShareButton.svelte';
import QrModal from '$lib/components/QrModal.svelte';
import RemainingTime from '$lib/components/RemainingTime.svelte';
import UrlField from '$lib/components/UrlField.svelte';
import { Button } from '$lib/components/ui/button';
import { remainingSecondsFrom } from '$lib/lifetime';
import { clearCreatedDelivery, takeCreatedDelivery } from '$lib/state/created-delivery';
import { cn } from '$lib/utils';

let { secretId }: { secretId: string } = $props();
const api = createManagementApiClient();
const statusCopy: Record<ManagementStatus, { title: string; detail: string }> = {
	// biome-ignore lint/style/useNamingConvention: public management status uses its canonical wire name.
	pending_upload: {
		title: 'Upload not finished',
		detail: 'The encrypted upload has not been finalized. You can cancel it.'
	},
	active: {
		title: 'Ready to open',
		detail: 'The recipient can open this delivery once before it expires.'
	},
	opened: {
		title: 'Opened',
		detail:
			'Flick released the encrypted content once. This does not confirm that anyone decrypted or read it.'
	},
	locked: {
		title: 'Locked',
		detail: 'Five incorrect access proofs exhausted access. This is not a confirmed opening.'
	},
	cancelled: {
		title: 'Cancelled',
		detail:
			'The recipient link can no longer open this delivery. Cancellation cannot recall copies already delivered.'
	},
	unavailable: { title: 'Unavailable', detail: 'The upload ended without an available delivery.' }
};
// The dot only decorates the title: green while openable, amber while uploading, grey once final.
function statusDot(status: ManagementStatus): string {
	if (status === 'active') {
		return 'bg-success ring-4 ring-success/20';
	}
	return status === 'pending_upload' ? 'bg-burn' : 'bg-muted-foreground';
}
let token = '';
let snapshot = $state<ManagementSnapshot | null>(null);
let recipientUrl = $state('');
let managementUrl = $state('');
let usesPassphrase = $state(false);
let busy = $state(false);
let unavailable = $state(false);
let expired = $state(false);
let errorMessage = $state('');
let notice = $state('');
let qrOpen = $state(false);
let heading = $state<HTMLHeadingElement | null>(null);
let now = $state(Date.now());
let controller: AbortController | null = null;
let lastRefreshAt = 0;
const remaining = $derived(remainingSecondsFrom(snapshot?.expires_at ?? '', now));
const canRefresh = $derived(!!managementUrl && !unavailable && !expired);

function discardDetails(): void {
	snapshot = null;
	recipientUrl = '';
	managementUrl = '';
	token = '';
	qrOpen = false;
	clearCreatedDelivery();
}

function accept(next: ManagementSnapshot): void {
	snapshot = next;
	errorMessage = '';
	if (!next.can_cancel) {
		recipientUrl = '';
		qrOpen = false;
	}
	tick();
}

function tick(): void {
	now = Date.now();
	if (snapshot && now >= Date.parse(snapshot.management_expires_at)) {
		expired = true;
		errorMessage = '';
		notice = '';
		controller?.abort();
		discardDetails();
	}
}

function report(error: unknown): void {
	if (error instanceof ManagementApiError && error.status === 404) {
		unavailable = true;
		errorMessage = '';
		discardDetails();
	} else if (error instanceof ManagementApiError && error.snapshot) {
		accept(error.snapshot);
		notice = 'The delivery changed before cancellation. Its current status is shown below.';
	} else {
		errorMessage =
			error instanceof ManagementApiError
				? error.message
				: 'Could not verify delivery status. Try again.';
	}
}

async function refresh(cancel = false): Promise<void> {
	if (busy || !canRefresh) {
		return;
	}
	busy = true;
	lastRefreshAt = Date.now();
	notice = '';
	const request = new AbortController();
	controller = request;
	try {
		// Refresh before offering irreversible cancellation; the API still resolves any later race.
		const fresh = await api.get(secretId, token, request.signal);
		if (request.signal.aborted) {
			return;
		}
		accept(fresh);
		if (cancel && !expired) {
			await cancelFreshDelivery(fresh, request.signal);
		}
	} catch (error) {
		if (!request.signal.aborted) {
			report(error);
		}
	} finally {
		if (controller === request) {
			controller = null;
			busy = false;
		}
	}
}

async function cancelFreshDelivery(fresh: ManagementSnapshot, signal: AbortSignal): Promise<void> {
	if (!fresh.can_cancel) {
		notice = 'This delivery can no longer be cancelled.';
		return;
	}
	if (
		!window.confirm(
			'Cancel this delivery permanently? The recipient link will stop working. This cannot recall content already delivered.'
		)
	) {
		return;
	}
	const result = await api.revoke(secretId, token, signal);
	if (!signal.aborted) {
		accept(result);
	}
}

onMount(() => {
	token = managementTokenFromFragment(window.location.hash) ?? '';
	if (token) {
		managementUrl = createManagementUrl(window.location.origin, secretId, token);
		const created = takeCreatedDelivery(secretId, token);
		recipientUrl = created?.recipientUrl ?? '';
		usesPassphrase = created?.usesPassphrase ?? false;
		void refresh();
	} else {
		unavailable = true;
		clearCreatedDelivery();
	}
	heading?.focus();
	const timer = window.setInterval(tick, 1000);
	const poll = () => {
		tick();
		if (
			!document.hidden &&
			Date.now() - lastRefreshAt >= 30_000 &&
			(!snapshot || snapshot.can_cancel)
		) {
			void refresh();
		}
	};
	const polling = window.setInterval(poll, 30_000);
	document.addEventListener('visibilitychange', poll);
	window.addEventListener('focus', poll);
	return () => {
		controller?.abort();
		window.clearInterval(timer);
		window.clearInterval(polling);
		document.removeEventListener('visibilitychange', poll);
		window.removeEventListener('focus', poll);
		discardDetails();
	};
});
</script>

<svelte:head>
	<title>Manage delivery - Flick</title>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<DeliveryFrame>
	<div class="grid gap-1.5">
		<p class="micro text-muted-foreground">private sender page</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{recipientUrl ? 'Link created' : 'Manage delivery'}</h1>
		<p class="text-sm text-muted-foreground">Check one delivery and cancel it before it is opened.</p>
	</div>
	{#if expired || unavailable}
		<p role="status" class="rounded-[14px] border bg-card px-5 py-[18px]">{expired ? 'Management link expired.' : 'Management link unavailable.'}</p>
	{:else}
		<section aria-label="Delivery status" class="grid gap-1 rounded-[14px] border bg-card px-5 py-[18px]">
			<div class="flex items-center justify-between gap-3">
				{#if snapshot}
					<h2 class="flex items-center gap-2.5 text-[17px] font-medium">
						<span class={cn('size-2 shrink-0 rounded-full', statusDot(snapshot.status))} aria-hidden="true"></span>
						{errorMessage ? 'Last known status: ' : ''}{statusCopy[snapshot.status].title}
					</h2>
				{:else}
					<p role="status">{busy ? 'Loading delivery status…' : 'Delivery status is unknown.'}</p>
				{/if}
				<Button variant="ghost" size="sm" class="text-[13px] text-muted-foreground" disabled={busy} onclick={() => { void refresh(); }}>Refresh status</Button>
			</div>
			{#if snapshot}
				<p class="pl-[18px] text-sm text-muted-foreground">{statusCopy[snapshot.status].detail}</p>
				<RemainingTime seconds={remaining} class="mt-1.5 ml-[18px]" />
			{/if}
			{#if errorMessage}<p role="alert" class="text-sm text-destructive">{errorMessage}</p>{/if}
			{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
		</section>
		{#if recipientUrl}
			<section aria-labelledby="recipient-link-heading" class="grid gap-2.5">
				<h2 id="recipient-link-heading" class="text-[15px] font-medium">Send to your recipient</h2>
				<UrlField value={recipientUrl} id="share-url" copyLabel="Copy recipient link" prominent />
				<div class="flex flex-wrap items-start gap-2">
					<NativeShareButton recipientUrl={recipientUrl} class="h-9 w-auto rounded-lg px-3 text-[13px]" />
					<Button variant="outline" size="seg" onclick={() => { qrOpen = true; }}><QrCodeIcon class="size-4" aria-hidden="true" />Show QR</Button>
				</div>
				<p class="text-[13px] text-muted-foreground">{usesPassphrase ? 'Send the passphrase separately. The recipient needs both the link and the passphrase.' : 'Anyone with this full link can open it once. Share only with your recipient.'}</p>
			</section>
		{:else if !snapshot || snapshot.can_cancel}
			<p class="text-sm text-muted-foreground">The recipient link is available only in the original creation session. Use the copy you saved or create a new delivery.</p>
		{/if}
		{#if managementUrl}
			<section aria-labelledby="private-link-heading" class="grid gap-2 rounded-[14px] border border-[color-mix(in_srgb,var(--burn)_28%,var(--border))] bg-[color-mix(in_srgb,var(--burn)_6%,var(--card))] px-5 py-[18px]">
				<h2 id="private-link-heading" class="text-[15px] font-medium">Keep this management link private</h2>
				<p class="text-[13px] leading-[1.55] text-muted-foreground">Anyone with this link can check or cancel this delivery until it expires. Save it now; a lost link cannot be recovered. It cannot decrypt the content or recover the recipient link.</p>
				<UrlField value={managementUrl} id="management-url" label="Private management link" copyLabel="Copy management link" />
			</section>
		{/if}
		{#if snapshot?.can_cancel}
			<Button variant="destructive" class="h-10 justify-self-center rounded-[10px] border-destructive/35 bg-transparent px-[18px] dark:bg-transparent" disabled={busy} onclick={() => { void refresh(true); }}>Cancel delivery</Button>
		{/if}
	{/if}
	<a href={resolve('/')} class="pb-3 text-center text-[13px] text-muted-foreground underline underline-offset-4">Create another</a>
</DeliveryFrame>
<QrModal bind:open={qrOpen} url={recipientUrl} />
