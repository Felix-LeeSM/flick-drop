<script lang="ts">
import { Switch as SwitchPrimitive } from 'bits-ui';
import { cn, type WithoutChildrenOrChild } from '#lib/utils.js';

let {
	ref = $bindable(null),
	checked = $bindable(false),
	class: className,
	...restProps
}: WithoutChildrenOrChild<SwitchPrimitive.RootProps> = $props();
</script>

<SwitchPrimitive.Root
	bind:ref
	bind:checked
	data-slot="switch"
	class={cn(
		'peer inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full border border-transparent shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input data-[state=unchecked]:border-border',
		className
	)}
	{...restProps}
>
	<SwitchPrimitive.Thumb
		class={cn(
			'pointer-events-none block size-5 rounded-full bg-background shadow transition-transform data-[state=checked]:translate-x-[1.25rem] data-[state=unchecked]:translate-x-0'
		)}
	/>
</SwitchPrimitive.Root>
