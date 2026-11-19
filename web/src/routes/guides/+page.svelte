<script lang="ts">
import SiteHeader from '#lib/components/SiteHeader.svelte';
import SponsorSlot from '#lib/components/SponsorSlot.svelte';
import { t } from '#lib/i18n/index.js';
import { getLocale } from '#lib/i18n/locale.svelte.js';
import { resolve } from '$app/paths';
import GuideEn from './GuideEn.svelte';
import GuideJa from './GuideJa.svelte';
import GuideKo from './GuideKo.svelte';

const repo = 'https://github.com/Felix-LeeSM/flick-drop';

// Ids are fixed: old guide URLs redirect to #sending (web/nginx.conf).
const sections = $derived([
	{ id: 'sending', label: t().guides.sections.sending },
	{ id: 'limits', label: t().guides.sections.limits },
	{ id: 'privacy', label: t().guides.sections.privacy },
	{ id: 'source', label: t().guides.sections.source },
	{ id: 'self-host', label: t().guides.sections.selfHost }
]);
</script>

<svelte:head>
	<title>{t().guides.title} - Flick</title>
	<meta name="description" content={t().guides.description} />
	<link rel="canonical" href={resolve('guides/')} />
</svelte:head>

<div class="mx-auto grid max-w-3xl grid-cols-[minmax(0,1fr)] px-5 py-6 text-foreground sm:px-8 sm:py-10">
	<SiteHeader />
	<main class="py-12 sm:py-20">
		<h1 class="max-w-2xl font-serif text-5xl leading-[1.06] sm:text-6xl">{t().guides.title}</h1>
		<p class="mt-6 max-w-xl text-lg leading-relaxed text-muted-foreground">{t().guides.description}</p>
		<a href={resolve('/')} class="mt-8 inline-flex min-h-11 items-center rounded-lg bg-primary px-5 py-3 text-sm font-medium text-primary-foreground focus-visible:outline-2 focus-visible:outline-offset-4">
			{t().guides.cta}
		</a>

		<nav aria-label={t().guides.toc} class="mt-12 border-l-2 pl-5 text-sm">
			<p class="text-xs font-medium uppercase tracking-widest text-muted-foreground">{t().guides.toc}</p>
			<ol class="mt-2">
				{#each sections as section (section.id)}
					<li><a href={`#${section.id}`} class="inline-block py-1.5 underline-offset-4 hover:underline">{section.label}</a></li>
				{/each}
			</ol>
		</nav>

		<article class="guide-copy mt-6 max-w-prose leading-7">
			{#if getLocale() === 'ko'}
				<GuideKo {repo} />
			{:else if getLocale() === 'ja'}
				<GuideJa {repo} />
			{:else}
				<GuideEn {repo} />
			{/if}
		</article>
		<SponsorSlot />
	</main>

	<nav aria-label={t().guides.footerNav} class="flex flex-wrap gap-x-6 gap-y-2 border-t pt-5 text-sm text-muted-foreground">
		<a href={resolve('/')} class="py-2 underline underline-offset-4">{t().guides.openFlick}</a>
		<a href={repo} class="py-2 underline underline-offset-4">{t().guides.sourceCode}</a>
	</nav>
</div>

<style>
.guide-copy :global(h2) { margin: 3rem 0 0.8rem; font-size: 1.5rem; font-weight: 600; line-height: 1.35; scroll-margin-top: 1.5rem; }
.guide-copy :global(h3) { margin: 1.75rem 0 0.4rem; font-size: 1.05rem; font-weight: 600; line-height: 1.45; }
.guide-copy :global(p) { margin: 0.6rem 0; }
.guide-copy :global(ol) { list-style: decimal; padding-left: 1.4rem; }
.guide-copy :global(ul) { list-style: disc; padding-left: 1.4rem; }
.guide-copy :global(li) { padding-left: 0.4rem; margin: 0.5rem 0; }
.guide-copy :global(a) { text-decoration: underline; text-underline-offset: 4px; overflow-wrap: anywhere; }
.guide-copy :global(aside) { margin: 1.75rem 0; border-left: 3px solid var(--primary); padding: 0.2rem 0 0.2rem 1.25rem; }
.guide-copy :global(code) { overflow-wrap: anywhere; font-size: 0.9em; }
.guide-copy :global(pre) { margin: 1rem 0; overflow-x: auto; border-radius: 0.5rem; background: var(--muted); padding: 1rem; font-size: 0.875rem; line-height: 1.6; }
.guide-copy :global(pre code) { overflow-wrap: normal; white-space: pre; }
.guide-copy :global(.guide-divider) { margin: 4rem 0 0; border-top: 1px solid var(--border); padding-top: 1rem; font-size: 0.75rem; font-weight: 500; letter-spacing: 0.1em; text-transform: uppercase; color: var(--muted-foreground); }
</style>
