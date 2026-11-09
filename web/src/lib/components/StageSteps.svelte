<script lang="ts">
import { CheckIcon } from '@lucide/svelte';
import type { Component } from 'svelte';
import { cn } from '#lib/utils.js';

type StageStep = { icon: Component; label: string };

type Props = {
	steps: StageStep[];
	/** Steps before this index are done; the step at this index is running. */
	activeIndex: number;
	class?: string;
};

let { steps, activeIndex, class: className }: Props = $props();
</script>

<ol class={cn('grid gap-1', className)} aria-label="Progress steps">
	{#each steps as step, index (step.label)}
		{@const StepIcon = step.icon}
		{@const done = index < activeIndex}
		{@const active = index === activeIndex}
		<li
			class={cn(
				'flex min-h-10 items-center gap-3 rounded-lg px-2.5 text-sm transition-colors duration-300 motion-reduce:transition-none',
				done && 'text-muted-foreground',
				active && 'bg-muted/40 font-medium text-foreground',
				!done && !active && 'text-muted-foreground'
			)}
			aria-current={active ? 'step' : undefined}
		>
			<span
				class={cn(
					'grid size-7 shrink-0 place-items-center rounded-full border transition-colors duration-300 motion-reduce:transition-none',
					done && 'border-transparent bg-[color-mix(in_srgb,var(--success)_15%,transparent)] text-success',
					active && 'border-primary/40 bg-primary/10 text-primary',
					!done && !active && 'border-border bg-muted/30'
				)}
			>
				{#if done}
					<CheckIcon class="size-3.5" strokeWidth={2.4} aria-hidden="true" />
				{:else}
					<StepIcon class="size-3.5" aria-hidden="true" />
				{/if}
			</span>
			<span class="min-w-0 truncate">{step.label}</span>
			{#if active}
				<span
					class="ml-auto size-1.5 shrink-0 rounded-full bg-primary motion-safe:animate-pulse"
					aria-hidden="true"
				></span>
			{/if}
		</li>
	{/each}
</ol>
