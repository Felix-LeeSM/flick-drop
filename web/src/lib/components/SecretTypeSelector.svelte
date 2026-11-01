<script lang="ts">
import {
	CreditCardIcon,
	FileUpIcon,
	IdCardIcon,
	KeyRoundIcon,
	ListPlusIcon,
	TypeIcon
} from '@lucide/svelte';
import { onMount } from 'svelte';
import { CREDENTIAL_TEMPLATES, type CredentialType } from '#lib/credentials/index.js';
import { cn } from '#lib/utils.js';

type SecretType = 'text' | 'file' | CredentialType;
let {
	value,
	disabled = false,
	onSelect
}: {
	value: SecretType;
	disabled?: boolean;
	onSelect: (value: SecretType) => void;
} = $props();

const icons = {
	'key-round': KeyRoundIcon,
	'credit-card': CreditCardIcon,
	'id-card': IdCardIcon,
	'list-plus': ListPlusIcon
};
const options: Array<{ type: SecretType; label: string; icon: typeof ListPlusIcon }> = [
	{ type: 'text', label: 'Text', icon: TypeIcon },
	{ type: 'file', label: 'File', icon: FileUpIcon },
	...CREDENTIAL_TEMPLATES.map((template) => ({
		type: template.type,
		label: template.label,
		icon: icons[template.icon as keyof typeof icons] ?? ListPlusIcon
	}))
];
// At 320px an unselected cell is about 36px wide, too narrow for "Identity", so that cell shows
// a short name until it is selected. Its accessible name stays the full name.
const shortLabels: Partial<Record<SecretType, string>> = { identity: 'ID' };

// Server-rendered buttons stay disabled until hydration attaches their click handlers.
let ready = $state(false);

onMount(() => {
	ready = true;
});
</script>

<!-- The selected type widens into its full name; the others share the rest of the row. -->
<div
	class="flex w-full gap-1 rounded-2xl border border-border bg-card p-1 shadow-2xs"
	role="group"
	aria-label="Secret type"
>
	{#each options as option (option.type)}
		{@const Icon = option.icon}
		{@const selected = value === option.type}
		{@const shown = selected ? option.label : (shortLabels[option.type] ?? option.label)}
		<button
			type="button"
			class={cn(
				'flex basis-0 flex-col items-center justify-center gap-1 rounded-xl py-2 outline-none transition-[flex-grow,background-color,color] duration-300 ease-out focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 motion-reduce:transition-none',
				selected
					? 'grow-[2.4] bg-foreground text-background'
					: 'grow text-muted-foreground hover:bg-muted/60 hover:text-foreground'
			)}
			data-type={option.type}
			aria-pressed={selected}
			disabled={!ready || disabled}
			onclick={() => onSelect(option.type)}
		>
			<Icon class="size-4" aria-hidden="true" />
			{#if shown === option.label}
				<span class="text-[0.6875rem] leading-none font-medium whitespace-nowrap">{shown}</span>
			{:else}
				<span class="text-[0.6875rem] leading-none font-medium whitespace-nowrap" aria-hidden="true">{shown}</span>
				<span class="sr-only">{option.label}</span>
			{/if}
		</button>
	{/each}
</div>
