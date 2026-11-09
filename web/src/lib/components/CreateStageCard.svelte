<script lang="ts">
import type { Component } from 'svelte';
import StageSteps from '#lib/components/StageSteps.svelte';
import SuccessCheck from '#lib/components/SuccessCheck.svelte';
import { Button } from '#lib/components/ui/button/index.js';

type StageStep = { icon: Component; label: string };

type Props = {
	steps: StageStep[];
	/** Index of the running step; a value past the last step renders the done beat. */
	step: number;
	/** The current create status sentence, shown under the stepper. */
	status: string;
	/** 0..1 upload fraction, or null when the running step has no measurable progress. */
	progress: number | null;
	/** Cancel is offered only while the encrypted PUT can still be aborted. */
	cancellable: boolean;
	onCancel: () => void;
};

let { steps, step, status, progress, cancellable, onCancel }: Props = $props();

const done = $derived(step >= steps.length);
const percent = $derived(progress === null ? 0 : Math.round(progress * 100));
</script>

<section
	class="grid gap-5 rounded-2xl border border-border bg-card p-6 shadow-sm"
	aria-label="Creating your link"
>
	<p class="micro text-muted-foreground">preparing your link</p>
	{#if done}
		<div class="grid place-items-center gap-3 py-2 animate-in fade-in zoom-in-95 duration-300">
			<SuccessCheck />
			<p class="text-sm font-medium">Link ready</p>
		</div>
	{:else}
		<StageSteps {steps} activeIndex={step} />
		{#if progress !== null}
			<div
				class="h-1 w-full overflow-hidden rounded-full bg-muted"
				role="progressbar"
				aria-label="Encrypted file upload progress"
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={percent}
			>
				<div
					class="h-full rounded-full bg-primary transition-[width] duration-200 ease-linear motion-reduce:transition-none"
					style="width: {percent}%"
				></div>
			</div>
		{/if}
		<p role="status" aria-live="polite" class="text-sm text-muted-foreground">{status}</p>
		{#if cancellable}
			<Button type="button" variant="outline" class="h-11 w-full" onclick={onCancel}>
				Cancel
			</Button>
		{/if}
	{/if}
</section>
