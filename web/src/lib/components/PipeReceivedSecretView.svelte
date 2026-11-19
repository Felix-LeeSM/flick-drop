<script lang="ts" module>
export type ReceivedPipePayload =
	| { kind: 'text'; code: string; text: string }
	| { kind: 'file'; code: string; fileBlob: Blob; fileName: string; fileSize: number };
</script>

<script lang="ts">
import { CheckIcon, CopyIcon, DownloadIcon, FileIcon } from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import CredentialView from '#lib/components/CredentialView.svelte';
import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
import * as Card from '#lib/components/ui/card/index.js';
import { parseCredential } from '#lib/credentials/index.js';
import { formatPipeCode } from '#lib/crypto/pipe.js';
import { t } from '#lib/i18n/index.js';
import { cn, formatBytes } from '#lib/utils.js';

let { payload, onDone }: { payload: ReceivedPipePayload; onDone: () => void } = $props();

const credential = $derived(payload.kind === 'text' ? parseCredential(payload.text) : null);

let heading: HTMLHeadingElement | undefined = $state();
let credentialView: CredentialView | undefined = $state();
let copied = $state(false);
let downloaded = $state(false);
let copyTimer: number | undefined;

let fileUrl = $state('');

$effect(() => {
	if (payload.kind !== 'file') {
		fileUrl = '';
		return;
	}
	const url = URL.createObjectURL(payload.fileBlob);
	fileUrl = url;
	return () => {
		URL.revokeObjectURL(url);
	};
});

onMount(() => {
	heading?.focus();
});

onDestroy(() => {
	if (copyTimer !== undefined) {
		window.clearTimeout(copyTimer);
	}
});

async function copySecret(): Promise<void> {
	if (payload.kind !== 'text') {
		return;
	}
	const copyText = credential ? credentialView?.copyText() : payload.text;
	if (!copyText || copyText.length === 0) {
		return;
	}
	try {
		await navigator.clipboard.writeText(copyText);
		copied = true;
		if (copyTimer !== undefined) {
			window.clearTimeout(copyTimer);
		}
		copyTimer = window.setTimeout(() => {
			copied = false;
		}, 2000);
	} catch {
		// Ignore clipboard failure in constrained contexts.
	}
}
</script>

<section class="grid gap-6">
	<div class="grid gap-1.5">
		<p class="micro flex items-center gap-1.5 text-success">
			<CheckIcon class="size-3.5 shrink-0" aria-hidden="true" />
			{t().live.received.label(formatPipeCode(payload.code))}
		</p>
		<h1 class="page-title outline-none" tabindex="-1" bind:this={heading}>{t().live.received.title}</h1>
		<p class="text-sm text-muted-foreground">
			{t().live.received.body}
		</p>
	</div>

	{#if payload.kind === 'text' && credential}
		<div class="grid gap-4">
			<CredentialView bind:this={credentialView} {credential} />
			<div class="grid gap-2.5 sm:grid-cols-2">
				<Button type="button" size="cta" class="w-full gap-2" onclick={copySecret}>
					{#if copied}
						<CheckIcon class="size-4 shrink-0" aria-hidden="true" />
						{t().live.received.copied}
					{:else}
						<CopyIcon class="size-4 shrink-0" aria-hidden="true" />
						{t().live.received.copyAll}
					{/if}
				</Button>
				<Button type="button" variant="outline" size="cta" class="w-full" onclick={onDone}>
					{t().live.received.done}
				</Button>
			</div>
		</div>
	{:else if payload.kind === 'text'}
		<div class="grid gap-4">
			<pre
				aria-label={t().live.received.decryptedContent}
				class="max-h-[26rem] overflow-auto rounded-lg border border-border bg-card p-6 font-mono text-[0.9375rem] leading-[1.65] whitespace-pre-wrap [overflow-wrap:anywhere] text-foreground shadow-xs"
			>{payload.text}</pre>
			<div class="grid gap-2.5 sm:grid-cols-2">
				<Button type="button" size="cta" class="w-full gap-2" onclick={copySecret}>
					{#if copied}
						<CheckIcon class="size-4 shrink-0" aria-hidden="true" />
						{t().live.received.copied}
					{:else}
						<CopyIcon class="size-4 shrink-0" aria-hidden="true" />
						{t().live.received.copySecret}
					{/if}
				</Button>
				<Button type="button" variant="outline" size="cta" class="w-full" onclick={onDone}>
					{t().live.received.done}
				</Button>
			</div>
		</div>
	{:else}
		<div class="grid gap-3">
			<Card.Root class="overflow-hidden">
				<Card.Header>
					<div class="flex min-w-0 items-center gap-3">
						<div class="grid size-10 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
							<FileIcon class="size-5" aria-hidden="true" />
						</div>
						<div class="min-w-0 flex-1">
							<Card.Title class="break-all text-base">{payload.fileName}</Card.Title>
							<Card.Description class="font-mono text-xs">
								{t().live.received.decryptedInBrowser(formatBytes(payload.fileSize))}
							</Card.Description>
						</div>
					</div>
				</Card.Header>
				<Card.Footer class="flex flex-wrap gap-2">
					<a
						class={cn(buttonVariants({ size: 'cta' }), 'w-full min-w-0 gap-2')}
						href={fileUrl}
						download={payload.fileName}
						rel="external"
						onclick={() => {
							downloaded = true;
						}}
					>
						{#if downloaded}
							<CheckIcon class="size-4 shrink-0" aria-hidden="true" />
							<span class="truncate">{t().live.received.saved(payload.fileName)}</span>
						{:else}
							<DownloadIcon class="size-4 shrink-0" aria-hidden="true" />
							<span class="truncate">{t().live.received.save(payload.fileName)}</span>
						{/if}
					</a>
				</Card.Footer>
			</Card.Root>
			<Button type="button" variant="outline" size="cta" class="w-full" onclick={onDone}>
				{t().live.received.done}
			</Button>
		</div>
	{/if}
</section>
