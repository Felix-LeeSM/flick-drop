<script lang="ts">
import { GlobeIcon } from '@lucide/svelte';
import { t } from '#lib/i18n/index.js';
import { chooseLocale, getLocale, LOCALE_NAMES, LOCALES } from '#lib/i18n/locale.svelte.js';

// The manual language switch at the foot of every page. Each name is written in its
// own language and marked with lang=, so a reader finds theirs whatever is showing.
</script>

<div
	role="group"
	aria-label={t().common.languageRow}
	class="flex flex-wrap items-center justify-center gap-x-1 pb-6 text-[0.8125rem] text-muted-foreground"
>
	<GlobeIcon class="mr-1 size-3.5" aria-hidden="true" />
	{#each LOCALES as code (code)}
		<button
			type="button"
			lang={code}
			class="inline-flex min-h-11 items-center rounded-sm px-2 hover:text-foreground focus-visible:outline-2 aria-pressed:font-semibold aria-pressed:text-foreground"
			aria-pressed={getLocale() === code}
			onclick={() => chooseLocale(code)}
		>
			{LOCALE_NAMES[code]}
		</button>
	{/each}
</div>
