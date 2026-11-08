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

type SecretType = 'text' | 'file' | CredentialType;
let {
	value,
	disabled = false,
	panelId,
	onSelect
}: {
	value: SecretType;
	disabled?: boolean;
	// Id of the panel a tab reveals, so each tab can carry aria-controls and
	// the page can label its role="tabpanel" with this tab's own id
	// (see CreateSecretPage.svelte / SubmitRequestPage.svelte).
	panelId: (type: SecretType) => string;
	onSelect: (value: SecretType) => void;
} = $props();

const icons = {
	'key-round': KeyRoundIcon,
	'credit-card': CreditCardIcon,
	'id-card': IdCardIcon,
	'list-plus': ListPlusIcon
};
const options: Array<{ type: SecretType; label: string; icon: typeof TypeIcon }> = [
	{ type: 'text', label: 'Text', icon: TypeIcon },
	{ type: 'file', label: 'File', icon: FileUpIcon },
	...CREDENTIAL_TEMPLATES.map((template) => ({
		type: template.type,
		label: template.label,
		icon: icons[template.icon as keyof typeof icons] ?? ListPlusIcon
	}))
];

// Server-rendered buttons stay disabled until hydration attaches their click handlers.
let ready = $state(false);

// Roving focus per the tablist standard, plus the sliding underline measured
// from the active tab (prototype round 30, T8).
let focusIndex = $state(0);
let tabEls: Array<HTMLElement | null> = [];
let underline = $state({ x: 0, w: 0 });

function measureUnderline(): void {
	const el = tabEls[options.findIndex((option) => option.type === value)];
	if (!el) {
		return;
	}
	underline = { x: el.offsetLeft, w: el.offsetWidth };
}

$effect(() => {
	void value;
	measureUnderline();
});

onMount(() => {
	ready = true;
	// Tab widths settle once the webfont finishes loading; the underline was
	// measured against fallback-metric widths at mount, so measure again.
	void document.fonts.ready.then(measureUnderline);
});

function tabKeydown(event: KeyboardEvent): void {
	const delta = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
	if (delta === 0) {
		return;
	}
	event.preventDefault();
	focusIndex = (focusIndex + delta + options.length) % options.length;
	const next = tabEls[focusIndex];
	next?.focus();
	next?.click();
}
</script>

<!-- Underline tab row: the indicator slides to the active tab and the row
     scrolls horizontally where six labels do not fit. min-w-0 keeps the
     min-w-max tab strip from stretching its grid track on narrow phones; the
     gradient fades the clipped edge instead of hinting that the last visible
     tab is the last tab. -->
<div class="relative min-w-0">
	<div
		class="-mx-1 overflow-x-auto border-b border-border"
		role="tablist"
		aria-label="Secret type"
		tabindex="-1"
		onkeydown={tabKeydown}
	>
		<div class="relative flex min-w-max gap-4 px-1">
			<span
				class="absolute -bottom-px left-0 h-0.5 bg-primary transition-[transform,width] duration-300 ease-out motion-reduce:transition-none"
				style="transform: translateX({underline.x}px); width: {underline.w}px;"
				aria-hidden="true"
			></span>
			{#each options as option, index (option.type)}
				{@const Icon = option.icon}
				{@const selected = value === option.type}
				<button
					type="button"
					role="tab"
					id={`${panelId(option.type)}-tab`}
					aria-selected={selected}
					aria-controls={panelId(option.type)}
					tabindex={index === focusIndex ? 0 : -1}
					bind:this={tabEls[index]}
					disabled={!ready || disabled}
					class="relative flex min-h-11 items-center gap-1.5 px-0.5 text-sm outline-none transition-colors motion-reduce:transition-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 {selected
						? 'font-medium text-foreground'
						: 'text-muted-foreground hover:text-foreground'}"
					onclick={() => {
						focusIndex = index;
						onSelect(option.type);
					}}
				>
					<Icon class="size-4" aria-hidden="true" />
					{option.label}
				</button>
			{/each}
		</div>
	</div>
	<div
		class="pointer-events-none absolute inset-y-0 right-0 w-8 bg-gradient-to-l from-background to-transparent"
		aria-hidden="true"
	></div>
</div>
