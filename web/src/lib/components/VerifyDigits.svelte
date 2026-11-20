<script lang="ts">
import { cn } from '#lib/utils.js';

// Server-drawn per receiver: internal/piperelay/coordinator.go generateVerifyCode.
type Props = {
	code: string;
	label: string;
	size?: 'lg' | 'sm';
};

let { code, label, size = 'lg' }: Props = $props();
</script>

<div
	class={cn('flex shrink-0 items-center justify-center', size === 'lg' ? 'gap-2' : 'gap-1')}
	role="img"
	aria-label={label}
>
	{#each code.split('') as digit, index (index)}
		<span
			class={cn(
				'grid place-items-center border-2 border-primary/40 bg-primary/8 font-mono font-extrabold text-primary shadow-xs',
				size === 'lg' ? 'aspect-square w-16 min-w-0 rounded-xl text-4xl' : 'size-8 rounded-lg text-base'
			)}
			aria-hidden="true"
		>
			{digit}
		</span>
	{/each}
</div>
