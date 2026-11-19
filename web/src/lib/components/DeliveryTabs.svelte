<script lang="ts">
import { t } from '#lib/i18n/index.js';
import { cn } from '#lib/utils.js';

// role="group" + aria-pressed, not a tablist: it switches form-level presentation, not a panel.
let {
	delivery = $bindable('link'),
	disabled = false
}: {
	delivery: 'link' | 'code';
	disabled?: boolean;
} = $props();

const tabs = $derived([
	{ value: 'link', label: t().common.tabLink },
	{ value: 'code', label: t().common.tabLive }
] as const);
</script>

<div class="relative border-b border-border" role="group" aria-label={t().common.deliveryMethod}>
	<div class="grid grid-cols-2">
		{#each tabs as tab (tab.value)}
			<button
				type="button"
				class={cn(
					'flex min-h-11 items-center justify-center text-[0.9375rem] outline-none transition-colors motion-reduce:transition-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50',
					delivery === tab.value
						? 'font-medium text-foreground'
						: 'text-muted-foreground hover:text-foreground'
				)}
				aria-pressed={delivery === tab.value}
				disabled={disabled}
				onclick={() => {
					delivery = tab.value;
				}}
			>
				{tab.label}
			</button>
		{/each}
	</div>
	<span
		class="absolute -bottom-px left-0 h-0.5 w-1/2 bg-primary transition-transform duration-300 ease-out motion-reduce:transition-none"
		style="transform: translateX({delivery === 'code' ? '100%' : '0%'});"
		aria-hidden="true"
	></span>
</div>
