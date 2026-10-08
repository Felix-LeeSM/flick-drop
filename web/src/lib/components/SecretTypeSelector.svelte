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
import { Button } from '$lib/components/ui/button';
import { CREDENTIAL_TEMPLATES, type CredentialType } from '$lib/credentials';
import { cn } from '$lib/utils';

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

let selector: HTMLDivElement;
let ready = $state(false);
let animate = $state(false);
let highlight = $state({ x: 0, y: 0, width: 0, height: 36 });

function moveHighlight(button: HTMLButtonElement): void {
	const parent = selector.getBoundingClientRect();
	const target = button.getBoundingClientRect();
	highlight = {
		x: target.left - parent.left - selector.clientLeft,
		y: target.top - parent.top - selector.clientTop,
		width: target.width,
		height: target.height
	};
}

function measureSelected(): void {
	const button = selector.querySelector<HTMLButtonElement>(`button[data-type="${value}"]`);
	if (button) {
		moveHighlight(button);
	}
}

onMount(() => {
	measureSelected();
	ready = true;
	const observer = new ResizeObserver(measureSelected);
	observer.observe(selector);
	for (const button of selector.querySelectorAll('button')) {
		observer.observe(button);
	}
	return () => observer.disconnect();
});

// A remounted form or a parent-driven selection uses the current button geometry.
$effect(() => {
	if (ready) {
		measureSelected();
	}
});
</script>

<div
	bind:this={selector}
	class="selector relative isolate flex w-fit max-w-full flex-wrap gap-1 rounded-full border border-border bg-card p-1.5 md:flex-nowrap"
	role="group"
	aria-label="Secret type"
	aria-busy={!ready}
>
	<span
		class="selection-indicator pointer-events-none absolute left-0 top-0 rounded-full bg-foreground"
		class:animate
		aria-hidden="true"
		style:transform={`translate(${highlight.x}px, ${highlight.y}px)`}
		style:width={`${highlight.width}px`}
		style:height={`${highlight.height}px`}
		style:visibility={ready ? 'visible' : 'hidden'}
	></span>
	{#each options as option (option.type)}
		{@const Icon = option.icon}
		<Button
			type="button"
			size="seg"
			variant="ghost"
			class={cn(
				'relative z-10 gap-2 rounded-full border-transparent bg-transparent px-2.5 hover:bg-transparent active:not-aria-[haspopup]:translate-y-0 dark:hover:bg-transparent',
				value === option.type ? 'text-background hover:text-background' : 'text-muted-foreground',
				!ready && value === option.type && 'bg-foreground'
			)}
			data-type={option.type}
			aria-pressed={value === option.type}
			disabled={!ready || disabled}
			onclick={() => {
				animate = true;
				onSelect(option.type);
			}}
		>
			<Icon class="size-4" aria-hidden="true" />
			<span>{option.label}</span>
		</Button>
	{/each}
</div>

<style>
.selection-indicator.animate {
	transition: transform 280ms cubic-bezier(0.22, 1, 0.36, 1), width 280ms cubic-bezier(0.22, 1, 0.36, 1);
}

.selector :global(button) {
	transition: color 160ms ease;
}

@media (prefers-reduced-motion: reduce) {
	.selection-indicator.animate,
	.selector :global(button) {
		transition: none;
	}
}
</style>
