<script lang="ts">
import { CheckIcon, GlobeIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { t } from '#lib/i18n/index.js';
import {
	chooseLocale,
	getLocale,
	LOCALE_NAMES,
	LOCALES,
	type Locale
} from '#lib/i18n/locale.svelte.js';

let open = $state(false);
let wrapEl = $state<HTMLDivElement | null>(null);

function pick(code: Locale): void {
	open = false;
	chooseLocale(code);
}

onMount(() => {
	const onKeyDown = (event: KeyboardEvent) => {
		if (event.key === 'Escape') {
			open = false;
		}
	};
	const onClick = (event: MouseEvent) => {
		if (open && wrapEl && !wrapEl.contains(event.target as Node)) {
			open = false;
		}
	};
	window.addEventListener('keydown', onKeyDown);
	document.addEventListener('click', onClick);
	return () => {
		window.removeEventListener('keydown', onKeyDown);
		document.removeEventListener('click', onClick);
	};
});
</script>

<div class="relative" bind:this={wrapEl}>
	<Button
		type="button"
		variant="outline"
		size="icon"
		class="rounded-lg bg-card dark:bg-card"
		data-language-toggle
		aria-label={t().common.languageButton(LOCALE_NAMES[getLocale()])}
		title={t().common.languageButton(LOCALE_NAMES[getLocale()])}
		aria-haspopup="menu"
		aria-expanded={open}
		onclick={(event: MouseEvent) => {
			event.stopPropagation();
			open = !open;
		}}
	>
		<GlobeIcon class="size-4" aria-hidden="true" />
	</Button>
	{#if open}
		<div
			role="menu"
			aria-label={t().common.languageRow}
			class="absolute top-[calc(100%+0.375rem)] right-0 z-50 min-w-34 max-w-[calc(100vw-1.5rem)] rounded-[10px] border border-border bg-popover p-1 text-popover-foreground shadow-lg"
		>
			{#each LOCALES as code (code)}
				<button
					type="button"
					role="menuitemradio"
					aria-checked={getLocale() === code}
					lang={code}
					class="flex min-h-9 w-full items-center justify-between gap-3 rounded-md px-2.5 py-1.5 text-left text-[0.8125rem] hover:bg-muted focus-visible:outline-2"
					onclick={() => pick(code)}
				>
					<span>{LOCALE_NAMES[code]}</span>
					{#if getLocale() === code}
						<CheckIcon class="size-3.5 text-primary" aria-hidden="true" />
					{/if}
				</button>
			{/each}
		</div>
	{/if}
</div>
