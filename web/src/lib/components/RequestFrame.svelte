<script lang="ts">
import { LockKeyholeIcon } from '@lucide/svelte';
import type { Snippet } from 'svelte';
import { resolve } from '$app/paths';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';

let { children }: { children: Snippet } = $props();
</script>

<div class="mx-auto max-w-xl px-5 py-8 sm:py-12">
	<header class="mb-10 flex items-center justify-between">
		<a href={resolve('/')} class="flex items-center gap-2.5 font-serif text-xl"><LockKeyholeIcon class="size-5" aria-hidden="true" />Flick</a>
		<ThemeToggle />
	</header>
	<main class="grid gap-7">{@render children()}</main>
</div>
