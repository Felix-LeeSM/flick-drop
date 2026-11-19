<script lang="ts">
import type { HTMLAttributes } from 'svelte/elements';
import { t } from '#lib/i18n/index.js';
import { formatRemaining } from '#lib/lifetime.js';
import { cn } from '#lib/utils.js';

// One remaining-time chip for the management, requester, and submit pages.
let {
	seconds,
	class: className,
	...rest
}: { seconds: number } & HTMLAttributes<HTMLParagraphElement> = $props();
// formatRemaining writes English unit letters ("6d 23h"); each is swapped for the current language's unit.
const label = $derived(
	t().manage.remaining(
		formatRemaining(seconds).replace(
			/(\d+)([dhms])/g,
			(_, amount: string, unit: 'd' | 'h' | 'm' | 's') => amount + t().manage.timeUnits[unit]
		)
	)
);
</script>

<p class={cn('justify-self-start whitespace-nowrap rounded-full bg-muted px-2.5 py-0.5 font-mono text-xs leading-5', className)} {...rest}>
	{label}
</p>
