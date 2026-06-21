<script lang="ts">
import { CheckIcon } from '@lucide/svelte';
import { cn } from '$lib/utils.js';

type Props = {
	class?: string;
};

let { class: className = '' }: Props = $props();
</script>

<span class={cn('check-circle', className)}>
	<CheckIcon class="size-7" strokeWidth={2.2} />
</span>

<style>
	.check-circle {
		width: 3.25rem;
		height: 3.25rem;
		border-radius: 9999px;
		display: grid;
		place-items: center;
		background: color-mix(in oklch, var(--success) 15%, transparent);
		color: var(--success);
	}
	.check-circle :global(svg) {
		width: 1.75rem;
		height: 1.75rem;
	}
	.check-circle :global(path) {
		stroke-dasharray: 30;
		stroke-dashoffset: 30;
		animation: flick-draw 0.55s cubic-bezier(0.65, 0, 0.35, 1) 0.15s forwards;
	}
	@keyframes flick-draw {
		to {
			stroke-dashoffset: 0;
		}
	}
</style>
