<script lang="ts">
import { cn } from '#lib/utils.js';

// The first control of the create page: which delivery the rest of the form
// shows. Text-only on purpose — the tab labels ("Create link" / "Send with
// code") already say what each flow is, and an icon here would repeat the lock
// glyph the button below uses. The page owns `delivery` so the heading can
// adapt to it.
let {
	delivery = $bindable('link'),
	disabled = false
}: {
	delivery: 'link' | 'code';
	disabled?: boolean;
} = $props();
</script>

<div class="grid grid-cols-2 gap-1 rounded-lg bg-muted/40 p-1" role="group" aria-label="Delivery method">
	<button
		type="button"
		class={cn(
			'flex h-9 items-center justify-center rounded-md text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50',
			delivery === 'link'
				? 'bg-card text-foreground shadow-xs'
				: 'text-muted-foreground hover:text-foreground'
		)}
		aria-pressed={delivery === 'link'}
		disabled={disabled}
		onclick={() => {
			delivery = 'link';
		}}
	>
		Create link
	</button>
	<button
		type="button"
		class={cn(
			'flex h-9 items-center justify-center rounded-md text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50',
			delivery === 'code'
				? 'bg-card text-foreground shadow-xs'
				: 'text-muted-foreground hover:text-foreground'
		)}
		aria-pressed={delivery === 'code'}
		disabled={disabled}
		onclick={() => {
			delivery = 'code';
		}}
	>
		Send with code
	</button>
</div>
