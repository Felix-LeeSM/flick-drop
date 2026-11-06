<script lang="ts">
import { InfoIcon, LockKeyholeIcon } from '@lucide/svelte';
import type { Snippet } from 'svelte';
import { Label } from '#lib/components/ui/label/index.js';
import { Switch } from '#lib/components/ui/switch/index.js';

// One opt-in protection row (prototype round 30, U2): the label toggles the
// switch, and the info button beside it pops the explanation on hover or
// keyboard focus. The info button is a sibling of the label, not a child —
// inside it, popover clicks would toggle the switch and the nesting would
// break HTML phrasing rules.
let {
	id,
	checked = $bindable(false),
	label,
	icon,
	infoLabel,
	describedBy,
	disabled = false,
	details,
	field
}: {
	id: string;
	checked?: boolean;
	label: string;
	icon?: typeof LockKeyholeIcon;
	infoLabel: string;
	describedBy: string;
	disabled?: boolean;
	details: Snippet;
	// Rendered under the row while the switch is on, e.g. the passphrase field.
	field?: Snippet;
} = $props();
</script>

<div class="grid gap-2.5">
	<div class="flex min-h-11 items-center gap-3">
		<div class="relative flex flex-1 items-center gap-1.5">
			<Label for={id} class="flex cursor-pointer items-center gap-1.5 text-sm font-medium">
				{#if icon}
					{@const Icon = icon}
					<Icon class="size-3.5 text-primary" aria-hidden="true" />
				{/if}
				{label}
			</Label>
			<div class="group ml-0.5 inline-flex">
				<button
					type="button"
					class="grid size-6 place-items-center rounded-full text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
					aria-label={infoLabel}
					aria-describedby={describedBy}
				>
					<InfoIcon class="size-3.5" aria-hidden="true" />
				</button>
				<div
					id={describedBy}
					role="tooltip"
					class="invisible absolute left-0 top-full z-20 mt-1.5 w-max max-w-[calc(100vw-2rem)] rounded-lg border border-border bg-card p-3 text-left opacity-0 shadow-lg transition-opacity duration-150 group-hover:visible group-hover:opacity-100 group-focus-within:visible group-focus-within:opacity-100 motion-reduce:transition-none"
				>
					{@render details()}
				</div>
			</div>
		</div>
		<span class="relative inline-flex">
			<Switch
				bind:checked
				{id}
				{disabled}
				class="relative before:absolute before:-inset-2.5 before:content-['']"
			/>
		</span>
	</div>
	{#if checked && field}
		{@render field()}
	{/if}
</div>
