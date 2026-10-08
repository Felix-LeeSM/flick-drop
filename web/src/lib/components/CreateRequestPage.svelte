<script lang="ts">
import { onMount } from 'svelte';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';
import { createRequestApiClient } from '$lib/api/requests';
import RequestFrame from '$lib/components/RequestFrame.svelte';
import { Button } from '$lib/components/ui/button';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
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
let ttl = $state(DEFAULT_TTL_SECONDS / 60);
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
	const seconds = ttl * 60;
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
<RequestFrame>
	<div class="grid gap-2">
		<h1 class="font-serif text-4xl" tabindex="-1" bind:this={heading}>Request a secret</h1>
		<p class="text-sm text-muted-foreground">Let someone send you text or one small file. Only your private retrieval link can decrypt it, once.</p>
	</div>
	<form class="grid gap-5" onsubmit={(event) => { event.preventDefault(); void create(); }}>
		<div class="grid gap-2">
			<Label for="request-lifetime">Expires after (minutes)</Label>
			<Input id="request-lifetime" type="number" min={MIN_TTL_SECONDS / 60} max={MAX_TTL_SECONDS / 60} step="1" bind:value={ttl} required disabled={busy} />
			<p class="text-sm text-muted-foreground">The deadline starts now. A submission does not extend it.</p>
		</div>
		<aside class="grid gap-2 rounded-xl bg-muted/40 p-5 text-sm">
			<strong>Save your private retrieval link.</strong>
			<p>It contains your decryption key. Anyone with the complete link can retrieve the content. Browser history and sync, clipboard managers, and screenshots may retain it.</p>
			<p>You can reopen the complete link on another device until it expires. Losing the link or its key means losing the content; Flick cannot recover it.</p>
		</aside>
		{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
		<Button type="submit" class="h-11" disabled={busy}>{busy ? 'Creating request…' : unknown ? 'Create a new request' : 'Create request'}</Button>
	</form>
	<a href={resolve('/')} class="py-3 text-center text-sm underline underline-offset-4">Send a secret instead</a>
</RequestFrame>
