<script lang="ts">
import { LockKeyholeIcon, ShieldCheckIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { createRequestApiClient } from '#lib/api/requests.js';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import LifetimePicker from '#lib/components/LifetimePicker.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import { Textarea } from '#lib/components/ui/textarea/index.js';
import {
	DEFAULT_TTL_SECONDS,
	formatTtlRange,
	MAX_TTL_SECONDS,
	MIN_TTL_SECONDS
} from '#lib/config/ttl.js';
import {
	generateRequestKeys,
	REQUEST_NOTE_MAX_BYTES,
	sealRequestNote,
	utf8ByteLength
} from '#lib/crypto/requests.js';
import { t } from '#lib/i18n/index.js';
import { getLocale } from '#lib/i18n/locale.svelte.js';
import {
	clearRequestHandoff,
	handOffRequest,
	retrievalUrl,
	submissionUrl
} from '#lib/state/request-links.js';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';

const api = createRequestApiClient();
let ttl = $state(DEFAULT_TTL_SECONDS);
const validTtl = $derived(
	Number.isInteger(ttl) && ttl >= MIN_TTL_SECONDS && ttl <= MAX_TTL_SECONDS
);
let note = $state('');
const noteBytes = $derived(utf8ByteLength(note));
const noteTooLong = $derived(noteBytes > REQUEST_NOTE_MAX_BYTES);
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
	if (busy || noteTooLong) {
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
		const [keys, sealedMemo] = await Promise.all([
			generateRequestKeys(),
			note.trim().length > 0 ? sealRequestNote(note) : Promise.resolve(null)
		]);
		if (!alive) {
			return;
		}
		sent = true;
		const created = await api.create(
			keys.public_key,
			seconds,
			request.signal,
			sealedMemo?.encryptedNote
		);
		if (!alive) {
			return;
		}
		const receive = retrievalUrl(
			location.origin,
			created.id,
			created.retrieval_token,
			keys.identity
		);
		handOffRequest({
			id: created.id,
			token: created.retrieval_token,
			submissionUrl: submissionUrl(
				location.origin,
				created.id,
				created.submission_token,
				keys.fingerprint,
				sealedMemo?.memoKey
			)
		});
		note = '';
		await goto(receive);
	} catch {
		if (alive) {
			unknown = sent;
			error = sent ? t().request.create.unconfirmed : t().request.create.unsupported;
		}
	} finally {
		if (alive) {
			busy = false;
		}
	}
}
</script>

<svelte:head><title>{t().request.create.pageTitle}</title><meta name="robots" content="noindex, nofollow" /><meta name="referrer" content="no-referrer" /></svelte:head>
<DeliveryFrame link={{ href: resolve('/'), label: t().request.create.headerLink }}>
	<div class="grid gap-1.5">
		<p class="micro flex items-center gap-1.5 text-muted-foreground">
			<ShieldCheckIcon class="size-3.5 text-primary" aria-hidden="true" />
			{t().request.shared.badge}
		</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{t().request.create.heading}</h1>
		<p class="text-sm text-muted-foreground">{t().request.create.intro}</p>
	</div>
	<form class="grid gap-6" onsubmit={(event) => { event.preventDefault(); void create(); }}>
		<div class="grid gap-2">
			<div class="flex items-center justify-between gap-2">
				<Label for="request-note" class="text-sm font-medium">{t().request.create.noteLabel}</Label>
				<span class="text-xs text-muted-foreground">{t().request.create.optional}</span>
			</div>
			<Textarea
				id="request-note"
				rows={3}
				class="min-h-24 resize-y"
				placeholder={t().request.create.notePlaceholder}
				bind:value={note}
				disabled={busy}
				autocomplete="off"
			/>
			{#if noteTooLong}
				<p role="alert" class="text-xs text-destructive">
					{t().request.create.noteTooLong(
						REQUEST_NOTE_MAX_BYTES.toLocaleString(getLocale()),
						noteBytes.toLocaleString(getLocale())
					)}
				</p>
			{:else}
				<p class="text-xs text-muted-foreground">
					{t().request.create.noteHint}
				</p>
			{/if}
		</div>
		<div class="grid gap-2.5">
			<LifetimePicker bind:seconds={ttl} disabled={busy} groupLabel={t().request.create.lifetimeGroup} />
			<p class="text-sm text-muted-foreground">{t().request.create.lifetimeHint}</p>
		</div>
		{#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
		<Button type="submit" size="cta" disabled={busy || !validTtl || noteTooLong}><LockKeyholeIcon class="size-4" aria-hidden="true" />{busy ? t().request.create.creating : unknown ? t().request.create.createNew : t().request.create.create}</Button>
	</form>
</DeliveryFrame>
