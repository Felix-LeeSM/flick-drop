<script lang="ts">
import type { HTMLAttributes } from 'svelte/elements';
import { formatRemaining } from '$lib/lifetime';
import { cn } from '$lib/utils';

// One remaining-time chip for the management, requester, and submit pages.
let {
	seconds,
	class: className,
	...rest
}: { seconds: number } & HTMLAttributes<HTMLParagraphElement> = $props();
</script>

<p class={cn('justify-self-start whitespace-nowrap rounded-full bg-muted px-2.5 py-0.5 font-mono text-xs leading-5', className)} {...rest}>
	{formatRemaining(seconds)} remaining
</p>
