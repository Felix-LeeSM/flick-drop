<script lang="ts" module>
export type HeaderLink = { href: string; label: string };
</script>

<script lang="ts">
import { LockKeyholeIcon, ZapIcon } from '@lucide/svelte';
import { resolve } from '$app/paths';
import ReceiveCodeModal from '#lib/components/ReceiveCodeModal.svelte';
import ThemeToggle from '#lib/components/ThemeToggle.svelte';
import { buttonVariants } from '#lib/components/ui/button/index.js';
import { cn } from '#lib/utils.js';

// Every page renders this header, so the wordmark and the one optional link keep one size and place.
// With large text the link and toggle wrap below the wordmark instead of widening the page.
// The link and toggle can also wrap onto separate rows from each other: a long link label at a
// large text-size setting must not push the toggle past the viewport edge either.
let { link, showEnterCode = true }: { link?: HeaderLink; showEnterCode?: boolean } = $props();
let receiveModalOpen = $state(false);
</script>

<header class="flex min-w-0 min-h-10 flex-wrap items-center justify-between gap-3">
	<a class="flex items-center gap-2.5 rounded-sm focus-visible:outline-2" href={resolve('/')}>
		<span class="grid size-8 place-items-center rounded-[9px] bg-primary text-primary-foreground">
			<LockKeyholeIcon class="size-[1.0625rem]" aria-hidden="true" />
		</span>
		<span class="font-serif text-[1.6875rem] leading-none tracking-[-0.005em]">Flick</span>
	</a>
	<div class="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2">
		{#if showEnterCode}
			<button
				type="button"
				class={cn(buttonVariants({ variant: 'outline', size: 'seg' }), 'bg-card dark:bg-card gap-1.5')}
				onclick={() => {
					receiveModalOpen = true;
				}}
			>
				<ZapIcon class="size-3.5 text-primary" aria-hidden="true" />
				<span>Enter code</span>
			</button>
		{/if}
		{#if link}
			<nav aria-label="Site" class="flex">
				<a class={cn(buttonVariants({ variant: 'outline', size: 'seg' }), 'bg-card dark:bg-card')} href={link.href}>{link.label}</a>
			</nav>
		{/if}
		<ThemeToggle />
	</div>
</header>

<ReceiveCodeModal bind:open={receiveModalOpen} />
