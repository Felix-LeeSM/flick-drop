<script lang="ts">
import { QrCodeIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import {
	createManagementApiClient,
	createManagementUrl,
	ManagementApiError,
	type ManagementSnapshot,
	type ManagementStatus,
	managementTokenFromFragment
} from '#lib/api/management.js';
import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import NativeShareButton from '#lib/components/NativeShareButton.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import RemainingTime from '#lib/components/RemainingTime.svelte';
import UrlField from '#lib/components/UrlField.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { t } from '#lib/i18n/index.js';
import { remainingSecondsFrom } from '#lib/lifetime.js';
import { clearCreatedDelivery, takeCreatedDelivery } from '#lib/state/created-delivery.js';
import { cn } from '#lib/utils.js';
import { beforeNavigate } from '$app/navigation';
import { resolve } from '$app/paths';

let { secretId }: { secretId: string } = $props();
const api = createManagementApiClient();
const statusCopy = $derived<Record<ManagementStatus, { title: string; detail: string }>>({
	// biome-ignore lint/style/useNamingConvention: public management status uses its canonical wire name.
	pending_upload: t().manage.statuses.pendingUpload,
	active: t().manage.statuses.active,
	locked: t().manage.statuses.locked,
	cancelled: t().manage.statuses.cancelled,
	unavailable: t().manage.statuses.unavailable
});
function statusDot(status: ManagementStatus): string {
	if (status === 'active') {
		return 'bg-success ring-4 ring-success/20';
	}
	return status === 'pending_upload' ? 'bg-burn' : 'bg-muted-foreground';
}
let token = '';
let snapshot = $state<ManagementSnapshot | null>(null);
let recipientUrl = $state('');
// The recipient link lives only in this tab; ask before leaving until it is handed off.
let recipientLinkHandedOff = $state(false);
function handOffRecipientLink(): void {
	recipientLinkHandedOff = true;
}
let cancelConfirmOpen = $state(false);
let pendingCancel: { fresh: ManagementSnapshot; signal: AbortSignal } | null = null;
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
		notice = t().manage.changedBeforeCancel;
	} else {
		errorMessage =
			error instanceof ManagementApiError ? error.message : t().errors.deliveryStatusUnverified;
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
			queueCancelFresh(fresh, request.signal);
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

function queueCancelFresh(fresh: ManagementSnapshot, signal: AbortSignal): void {
	if (!fresh.can_cancel) {
		notice = t().manage.noLongerCancellable;
		return;
	}
	pendingCancel = { fresh, signal };
	cancelConfirmOpen = true;
}

async function cancelPendingDelivery(): Promise<void> {
	const pending = pendingCancel;
	pendingCancel = null;
	if (!pending || pending.signal.aborted) {
		return;
	}
	// Re-entry guard: keep buttons disabled for the whole revoke.
	busy = true;
	try {
		const result = await api.revoke(secretId, token, pending.signal);
		if (!pending.signal.aborted) {
			accept(result);
		}
	} catch (cause) {
		if (!pending.signal.aborted) {
			report(cause);
		}
	} finally {
		busy = false;
	}
}

// Cancelling a leave navigation shows the browser's own unload prompt.
beforeNavigate((navigation) => {
	if (recipientUrl === '' || recipientLinkHandedOff) {
		return;
	}
	if (navigation.type === 'leave') {
		navigation.cancel();
		return;
	}
	if (!window.confirm(t().manage.leavePrompt)) {
		navigation.cancel();
	}
});

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
	<title>{t().manage.headTitle} - Flick</title>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<DeliveryFrame>
	<div class="grid gap-1.5">
		<p class="micro text-muted-foreground">{t().manage.privateSenderPage}</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{recipientUrl ? t().manage.titleCreated : t().manage.titleManage}</h1>
		<p class="text-sm text-muted-foreground">{t().manage.intro}</p>
	</div>
	{#if expired || unavailable}
		<p role="status" class="rounded-[14px] border bg-card px-5 py-4.5">{expired ? t().manage.expired : t().errors.managementUnavailable}</p>
	{:else}
		<section aria-label={t().manage.statusRegion} class="grid gap-1 rounded-[14px] border bg-card px-5 py-4.5">
			<div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
				{#if snapshot}
					<h2 class="flex grow basis-32 items-center gap-2.5 text-[1.0625rem] font-medium">
						<span class={cn('size-2 shrink-0 rounded-full', statusDot(snapshot.status))} aria-hidden="true"></span>
						{errorMessage ? t().manage.lastKnown : ''}{statusCopy[snapshot.status].title}
					</h2>
				{:else}
					<p role="status">{busy ? t().manage.loadingStatus : t().manage.unknownStatus}</p>
				{/if}
				<Button variant="ghost" size="sm" class="ml-auto text-[0.8125rem] text-muted-foreground" disabled={busy} onclick={() => { void refresh(); }}>{t().manage.refresh}</Button>
			</div>
			{#if snapshot}
				<p class="pl-4.5 text-sm text-muted-foreground">{statusCopy[snapshot.status].detail}</p>
				<RemainingTime seconds={remaining} class="mt-1.5 ml-4.5" />
			{/if}
			{#if errorMessage}<p role="alert" class="text-sm text-destructive">{errorMessage}</p>{/if}
			{#if notice}<p role="status" class="text-sm">{notice}</p>{/if}
		</section>
		{#if recipientUrl}
			<section aria-labelledby="recipient-link-heading" class="grid gap-2.5" oncopy={handOffRecipientLink}>
				<h2 id="recipient-link-heading" class="text-[0.9375rem] font-medium">{t().manage.sendToRecipient}</h2>
				<UrlField value={recipientUrl} id="share-url" copyLabel={t().manage.copyRecipientLink} prominent oncopied={handOffRecipientLink} />
				<div class="flex flex-wrap items-start gap-2">
					<NativeShareButton recipientUrl={recipientUrl} onshared={handOffRecipientLink} class="h-9 w-auto justify-self-start rounded-lg px-3 text-[0.8125rem]" />
					<Button variant="outline" size="seg" onclick={() => { qrOpen = true; handOffRecipientLink(); }}><QrCodeIcon class="size-4" aria-hidden="true" />{t().manage.showQr}</Button>
				</div>
				<p class="text-[0.8125rem] text-muted-foreground">{usesPassphrase ? t().manage.sendPassphraseSeparately : t().manage.anyoneWithLink}</p>
			</section>
		{:else if !snapshot || snapshot.can_cancel}
			<p class="text-sm text-muted-foreground">{t().manage.recipientLinkOnlyOriginal}</p>
		{/if}
		{#if managementUrl}
			<section aria-labelledby="private-link-heading" class="grid gap-2 rounded-[14px] border border-[color-mix(in_srgb,var(--burn)_28%,var(--border))] bg-[color-mix(in_srgb,var(--burn)_6%,var(--card))] px-5 py-4.5">
				<h2 id="private-link-heading" class="text-[0.9375rem] font-medium">{t().manage.keepPrivate}</h2>
				<p class="text-[0.8125rem] leading-[1.55] text-muted-foreground">{t().manage.keepPrivateDetail}</p>
				<UrlField value={managementUrl} id="management-url" label={t().manage.privateLinkLabel} copyLabel={t().manage.copyManagementLink} />
			</section>
		{/if}
		{#if snapshot?.can_cancel}
			<Button variant="destructive" class="h-10 justify-self-center rounded-[10px] border-destructive/35 bg-transparent px-4.5 dark:bg-transparent" disabled={busy} onclick={() => { void refresh(true); }}>{t().manage.cancelDelivery}</Button>
		{/if}
	{/if}
	<a href={resolve('/')} class="pb-3 text-center text-[0.8125rem] text-muted-foreground underline underline-offset-4">{t().manage.createAnother}</a>
</DeliveryFrame>
<QrModal bind:open={qrOpen} url={recipientUrl} />
<ConfirmDialog
	bind:open={cancelConfirmOpen}
	title={t().manage.confirmTitle}
	description={t().manage.confirmDescription}
	confirmLabel={t().manage.cancelDelivery}
	onconfirm={() => { void cancelPendingDelivery(); }}
/>
