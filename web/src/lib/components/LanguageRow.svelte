<script lang="ts">
import { CheckIcon, GlobeIcon } from '@lucide/svelte';
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
let toggleEl = $state<HTMLButtonElement | null>(null);

function close(refocus: boolean): void {
	open = false;
	if (refocus) {
		toggleEl?.focus();
	}
}

function pick(code: Locale): void {
	chooseLocale(code);
	close(true);
}

function outside(target: EventTarget | null): boolean {
	return !(target instanceof Node && wrapEl?.contains(target));
}
</script>

<svelte:window
	onkeydown={(event) => {
		if (open && event.key === 'Escape') close(true);
	}}
/>

<svelte:document
	onclick={(event) => {
		if (open && outside(event.target)) close(false);
	}}
/>

<div
	class="relative"
	bind:this={wrapEl}
	onfocusout={(event) => {
		if (open && event.relatedTarget && outside(event.relatedTarget)) close(false);
	}}
>
	<Button
		bind:ref={toggleEl}
		type="button"
		variant="outline"
		size="icon"
		class="rounded-lg bg-card dark:bg-card"
		data-language-toggle
		aria-label={t().common.languageButton(LOCALE_NAMES[getLocale()])}
		title={t().common.languageButton(LOCALE_NAMES[getLocale()])}
		aria-expanded={open}
		aria-controls={open ? 'language-list' : undefined}
		onclick={(event: MouseEvent) => {
			event.stopPropagation();
			open = !open;
		}}
	>
		<GlobeIcon class="size-4" aria-hidden="true" />
	</Button>
	{#if open}
		<div
			id="language-list"
			role="group"
			aria-label={t().common.languageRow}
			class="absolute top-[calc(100%+0.375rem)] right-0 z-50 min-w-34 max-w-[calc(100vw-1.5rem)] rounded-[10px] border border-border bg-popover p-1 text-popover-foreground shadow-lg"
		>
			{#each LOCALES as code (code)}
				<button
					type="button"
					aria-pressed={getLocale() === code}
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
