<script lang="ts">
import { cn } from '#lib/utils.js';

// The live relay's 3-digit verification number, drawn per receiver connection
// by the server (internal/piperelay/coordinator.go generateVerifyCode). The
// receiver's dialog and the sender's waiting list show it in the same key-cap
// shape, so people compare shape to shape.
type Props = {
	code: string;
	/** Accessible name; the caps themselves are hidden from assistive tech. */
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
				// 큰 숫자는 글자를 키운 좁은 대화상자에서 정사각형을 유지한 채 폭에 맞춰 줄어든다.
				size === 'lg' ? 'aspect-square w-16 min-w-0 rounded-xl text-4xl' : 'size-8 rounded-lg text-base'
			)}
			aria-hidden="true"
		>
			{digit}
		</span>
	{/each}
</div>
