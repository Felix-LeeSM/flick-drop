<script lang="ts" module>
export type HeaderLink = { href: string; label: string };
</script>

<script lang="ts">
import { LockKeyholeIcon } from '@lucide/svelte';
import { resolve } from '$app/paths';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';
import { buttonVariants } from '$lib/components/ui/button';
import { cn } from '$lib/utils';

// Every page renders this header, so the wordmark and the one optional link keep one size and place.
let { link }: { link?: HeaderLink } = $props();
</script>

<header class="flex min-h-10 items-center justify-between gap-3">
	<a class="flex items-center gap-2.5 rounded-sm focus-visible:outline-2" href={resolve('/')}>
		<span class="grid size-8 place-items-center rounded-[9px] bg-primary text-primary-foreground">
			<LockKeyholeIcon class="size-[17px]" aria-hidden="true" />
		</span>
		<span class="font-serif text-[27px] leading-none tracking-[-0.005em]">Flick</span>
	</a>
	<div class="flex items-center gap-2">
		{#if link}
			<nav aria-label="Site" class="flex">
				<a class={cn(buttonVariants({ variant: 'outline', size: 'seg' }), 'bg-card dark:bg-card')} href={link.href}>{link.label}</a>
			</nav>
		{/if}
		<ThemeToggle />
	</div>
</header>
