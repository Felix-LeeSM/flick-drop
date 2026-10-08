<script lang="ts">
import { LockKeyholeIcon } from '@lucide/svelte';
import type { Snippet } from 'svelte';
import { base, resolve } from '$app/paths';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';

let {
	title,
	description,
	path,
	children
}: {
	title: string;
	description: string;
	path: string;
	children: Snippet;
} = $props();
</script>

<svelte:head>
	<title>{title} - Flick</title>
	<meta name="description" content={description} />
	<link rel="canonical" href={`${base}${path}`} />
</svelte:head>

<div class="mx-auto max-w-3xl px-5 py-6 text-foreground sm:px-8 sm:py-10">
	<header class="flex items-center justify-between gap-4">
		<a href={resolve('/')} class="flex items-center gap-2.5 rounded-sm focus-visible:outline-2">
			<span class="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground">
				<LockKeyholeIcon class="size-4" aria-hidden="true" />
			</span>
			<span class="font-serif text-xl">Flick</span>
		</a>
		<ThemeToggle />
	</header>
	<main class="py-12 sm:py-20">
		<h1 class="max-w-2xl font-serif text-5xl leading-[1.06] sm:text-6xl">{title}</h1>
		<p class="mt-6 max-w-xl text-lg leading-relaxed text-muted-foreground">{description}</p>
		<a href={resolve('/')} class="mt-8 inline-flex min-h-11 items-center rounded-lg bg-primary px-5 py-3 text-sm font-medium text-primary-foreground focus-visible:outline-2 focus-visible:outline-offset-4">
			Create a one-time link
		</a>
		<article class="guide-copy mt-14 max-w-prose leading-7">
			{@render children()}
		</article>
	</main>
	<nav aria-label="Sharing guides" class="flex flex-wrap gap-x-6 gap-y-2 border-t pt-5 text-sm text-muted-foreground">
		<a href={`${base}/guides/password-sharing/`} class="py-2 underline underline-offset-4">Sharing passwords</a>
		<a href={`${base}/guides/temporary-file-sharing/`} class="py-2 underline underline-offset-4">Sharing temporary files</a>
		<a href={resolve('/')} class="py-2 underline underline-offset-4">Open Flick</a>
	</nav>
</div>

<style>
:global(.guide-copy h2) { margin: 2.5rem 0 0.8rem; font-size: 1.35rem; font-weight: 600; line-height: 1.4; }
:global(.guide-copy p) { margin: 0.85rem 0; }
:global(.guide-copy ol) { list-style: decimal; padding-left: 1.4rem; }
:global(.guide-copy li) { padding-left: 0.4rem; margin: 0.85rem 0; }
:global(.guide-copy aside) { margin: 1.75rem 0; border-left: 3px solid var(--primary); padding: 0.2rem 0 0.2rem 1.25rem; }
:global(.guide-copy code) { overflow-wrap: anywhere; font-size: 0.9em; }
</style>
