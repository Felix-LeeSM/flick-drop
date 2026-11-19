<script lang="ts" module>
export type HeaderLink = { href: string; label: string };
</script>

<script lang="ts">
import { LockKeyholeIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { resolve } from '$app/paths';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import LanguageRow from '#lib/components/LanguageRow.svelte';
import type { ReceivedPipePayload } from '#lib/components/PipeReceivedSecretView.svelte';
import ReceiveCodeModal from '#lib/components/ReceiveCodeModal.svelte';
import ThemeToggle from '#lib/components/ThemeToggle.svelte';
import { t } from '#lib/i18n/index.js';

// Every page renders this header, so the wordmark and the one optional link keep one size and place.
// With large text the link and toggle wrap below the wordmark instead of widening the page.
// The link and toggle can also wrap onto separate rows from each other: a long link label at a
// large text-size setting must not push the toggle past the viewport edge either.
// The code action and the link read as underlined text, but each keeps the toggle's 36px row height
// as its tap target.
let {
	link,
	showEnterCode = false,
	receiveModalOpen = $bindable(false),
	onCodeReceived
}: {
	link?: HeaderLink;
	showEnterCode?: boolean;
	receiveModalOpen?: boolean;
	onCodeReceived?: (payload: ReceivedPipePayload) => void;
} = $props();
let initialReceiveCode = $state('');
// "Enter Code" and the page link are the header's two text actions and share one look.
const headerActionClass =
	'inline-flex h-9 items-center rounded-sm text-xs font-medium whitespace-nowrap text-foreground underline decoration-primary decoration-2 underline-offset-[6px] focus-visible:outline-2';

$effect(() => {
	if (!receiveModalOpen) {
		initialReceiveCode = '';
	}
});

onMount(() => {
	function checkHash(): void {
		if (typeof window === 'undefined') return;
		if (window.location.pathname !== '/') return;
		const hash = window.location.hash;
		if (hash.startsWith('#code=')) {
			const code = hash.slice('#code='.length);
			if (code) {
				initialReceiveCode = code;
				receiveModalOpen = true;
				history.replaceState(null, '', window.location.pathname + window.location.search);
			}
		}
	}
	checkHash();
	window.addEventListener('hashchange', checkHash);
	return () => {
		window.removeEventListener('hashchange', checkHash);
	};
});
</script>

<header class="flex min-w-0 min-h-10 flex-wrap items-center justify-between gap-3">
	<a
		class="flex items-center gap-2.5 rounded-sm focus-visible:outline-2"
		href={resolve('/')}
	>
		<span class="grid size-8 place-items-center rounded-[9px] bg-primary text-primary-foreground">
			<LockKeyholeIcon class="size-[1.0625rem]" aria-hidden="true" />
		</span>
		<span class="font-serif text-[1.6875rem] leading-none tracking-[-0.005em]">Flick</span>
	</a>
	<div class="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-x-3 gap-y-2">
		{#if showEnterCode}
			<button
				type="button"
				class={headerActionClass}
				onclick={() => {
					receiveModalOpen = true;
				}}
			>
				{t().common.enterCode}
			</button>
		{/if}
		{#if link}
			<nav aria-label={t().common.siteNav} class="flex">
				<a
					class={headerActionClass}
					href={link.href}>{link.label}</a
				>
			</nav>
		{/if}
		<div class="flex shrink-0 items-center gap-1.5">
			<LanguageRow />
			<ThemeToggle />
		</div>
	</div>
</header>

<ReceiveCodeModal
	bind:open={receiveModalOpen}
	initialCode={initialReceiveCode}
	apiBaseUrl={DEFAULT_API_BASE_URL}
	{onCodeReceived}
/>
