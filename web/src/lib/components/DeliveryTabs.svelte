<script lang="ts">
import { cn } from '#lib/utils.js';

// The first control of the create page: which delivery the rest of the form
// shows. Text-only on purpose — the tab labels ("Create link" / "Send with
// code") already say what each flow is. The underline indicator slides to the
// active tab, matching the secret type row's idiom (prototype round 30, H1).
// The page owns `delivery` so the heading can adapt to it.
//
// ARIA idiom (#335): this pair stays role="group" + aria-pressed on purpose.
// It looks like the type tablist below it, but it switches form-level
// presentation (heading, toggles, CTA, footer) instead of swapping a
// dedicated panel, so a tablist/tabpanel contract would have no panel to
// point at; toggle buttons announce their pressed state directly. The type
// row, which does swap an editor panel, is the real tablist.
let {
	delivery = $bindable('link'),
	disabled = false
}: {
	delivery: 'link' | 'code';
	disabled?: boolean;
} = $props();

const tabs = [
	{ value: 'link', label: 'Create link' },
	{ value: 'code', label: 'Send with code' }
] as const;
</script>

<div class="relative border-b border-border" role="group" aria-label="Delivery method">
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
