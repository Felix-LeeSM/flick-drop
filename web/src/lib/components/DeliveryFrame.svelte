<script lang="ts">
import { LockKeyholeIcon } from '@lucide/svelte';
import type { Snippet } from 'svelte';
import { resolve } from '$app/paths';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';

let { children, navigation }: { children: Snippet; navigation?: Snippet } = $props();
</script>

<main class="min-h-screen px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl gap-8">
		<header class="flex items-center justify-between gap-3">
			<a class="flex items-center gap-2.5" href={resolve('/')}>
				<span class="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground">
					<LockKeyholeIcon class="size-4" aria-hidden="true" />
				</span>
				<span class="font-serif text-lg leading-none">Flick</span>
			</a>
			<div class="flex items-center gap-2">
				{#if navigation}{@render navigation()}{/if}
				<ThemeToggle />
			</div>
		</header>
		<div class="grid gap-7">{@render children()}</div>
	</div>
</main>
