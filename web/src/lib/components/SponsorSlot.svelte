<script lang="ts">
import { t } from '#lib/i18n/index.js';
import { SPONSOR, type Sponsor, validateSponsor } from './sponsor';

let { config = SPONSOR }: { config?: Sponsor | null } = $props();
const sponsor = $derived(validateSponsor(config));
</script>

{#if sponsor}
	<aside aria-label={t().common.sponsor.label} class="mt-14 break-words rounded-xl border bg-muted/30 p-6">
		<p class="text-xs font-medium uppercase tracking-widest text-muted-foreground">{t().common.sponsor.label}</p>
		<a href={sponsor.url} target="_blank" rel="sponsored noopener noreferrer" referrerpolicy="no-referrer" class="mt-3 inline-block rounded-sm py-2 font-medium underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-offset-4">
			{sponsor.name}<span class="sr-only">{t().common.sponsor.opensNewTab}</span>
		</a>
		<p class="mt-1 max-w-prose text-sm leading-relaxed text-muted-foreground">{sponsor.description}</p>
	</aside>
{/if}
