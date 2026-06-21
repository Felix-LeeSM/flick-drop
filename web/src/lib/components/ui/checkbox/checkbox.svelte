<script lang="ts">
import { CheckIcon } from '@lucide/svelte';
import { Checkbox as CheckboxPrimitive } from 'bits-ui';
import { cn, type WithoutChildrenOrChild } from '$lib/utils.js';

let {
	ref = $bindable(null),
	checked = $bindable(false),
	class: className,
	...restProps
}: WithoutChildrenOrChild<CheckboxPrimitive.RootProps> = $props();
</script>

<CheckboxPrimitive.Root
	bind:ref
	bind:checked
	data-slot="checkbox"
	class={cn(
		'peer size-4 shrink-0 rounded-sm border border-primary shadow focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground',
		className
	)}
	{...restProps}
>
	{#if checked}
		<CheckIcon class="size-3.5" strokeWidth={3} />
	{/if}
</CheckboxPrimitive.Root>
