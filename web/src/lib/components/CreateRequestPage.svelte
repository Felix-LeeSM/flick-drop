<script lang="ts">
import { LockKeyholeIcon, ShieldCheckIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';
import { createRequestApiClient } from '$lib/api/requests';
import DeliveryFrame from '$lib/components/DeliveryFrame.svelte';
import LifetimePicker from '$lib/components/LifetimePicker.svelte';
import { Button } from '$lib/components/ui/button';
import {
	DEFAULT_TTL_SECONDS,
	formatTtlRange,
	MAX_TTL_SECONDS,
	MIN_TTL_SECONDS
} from '$lib/config/ttl';
import {
	exportRequestPrivateKey,
	exportRequestPublicKey,
	generateRequestKeys
} from '$lib/crypto/requests';
import {
	clearRequestHandoff,
	handOffRequest,
	retrievalUrl,
	submissionUrl
} from '$lib/state/request-links';

const api = createRequestApiClient();
let ttl = $state(DEFAULT_TTL_SECONDS);
const validTtl = $derived(
	Number.isInteger(ttl) && ttl >= MIN_TTL_SECONDS && ttl <= MAX_TTL_SECONDS
);
let busy = $state(false);
let error = $state('');
let unknown = $state(false);
let heading = $state<HTMLHeadingElement | null>(null);
let controller: AbortController | null = null;
let alive = false;

onMount(() => {
	alive = true;
	clearRequestHandoff();
	heading?.focus();
	return () => {
		alive = false;
		controller?.abort();
	};
});

async function create(): Promise<void> {
	if (busy) {
		return;
	}
	const seconds = ttl;
	if (!Number.isInteger(seconds) || seconds < MIN_TTL_SECONDS || seconds > MAX_TTL_SECONDS) {
		error = formatTtlRange(MIN_TTL_SECONDS, MAX_TTL_SECONDS);
		return;
	}
	busy = true;
	error = '';
	unknown = false;
	let sent = false;
	const request = new AbortController();
	controller = request;
	try {
		const keys = await generateRequestKeys();
		const publicKey = await exportRequestPublicKey(keys.publicKey);
		const privateKey = await exportRequestPrivateKey(keys.privateKey);
		if (!alive) {
			return;
		}
		sent = true;
		const created = await api.create(publicKey.public_key, seconds, request.signal);
		if (!alive) {
			return;
		}
		const receive = retrievalUrl(location.origin, created.id, created.retrieval_token, privateKey);
		handOffRequest({
			id: created.id,
			token: created.retrieval_token,
			submissionUrl: submissionUrl(
				location.origin,
				created.id,
				created.submission_token,
				publicKey.fingerprint
			)
		});
		await goto(receive);
	} catch {
		if (alive) {
			unknown = sent;
			error = sent
				? 'Creation was not confirmed. An unused request may exist and will expire. Its links cannot be recovered. Nothing is retried automatically.'
				: 'Request encryption is unavailable. Use a browser with Web Crypto over HTTPS or localhost.';
		}
	} finally {
		if (alive) {
			busy = false;
		}
	}
}
</script>

<svelte:head><title>Request a secret - Flick</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<DeliveryFrame>
	{#snippet navigation()}
		<a href={resolve('/')} class="px-2 py-3 text-sm underline underline-offset-4">Send a secret</a>
	{/snippet}
	<div class="grid gap-1.5">
		<p class="micro flex items-center gap-1.5 text-muted-foreground">
			<ShieldCheckIcon class="size-3.5" aria-hidden="true" />
			end-to-end encrypted · one-time request
		</p>
		<h1 class="font-serif text-3xl outline-none sm:text-4xl" tabindex="-1" bind:this={heading}>Request a secret</h1>
		<p class="text-sm text-muted-foreground">Get a link for someone to send you text or a file, once.</p>
	</div>
	<form class="grid gap-5" onsubmit={(event) => { event.preventDefault(); void create(); }}>
		<div class="grid gap-2.5">
			<LifetimePicker bind:seconds={ttl} disabled={busy} groupLabel="Request lifetime" />
			<p class="text-sm text-muted-foreground">Starts when you create the link. Submitting does not extend it.</p>
		</div>
		{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
		<Button type="submit" class="h-11 w-full shadow-lg shadow-primary/25" disabled={busy || !validTtl}><LockKeyholeIcon class="size-4" aria-hidden="true" />{busy ? 'Creating request…' : unknown ? 'Create a new request' : 'Create request'}</Button>
	</form>
</DeliveryFrame>
