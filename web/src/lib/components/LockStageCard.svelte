<script lang="ts">
import { CheckIcon, LoaderCircleIcon, XIcon } from '@lucide/svelte';
import ProgressRingLock from '#lib/components/ProgressRingLock.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { STAGE_VISUAL, type StagePhase } from '#lib/stage-timing.js';
import { cn } from '#lib/utils.js';

// Stage card shown in place of the form while a secret is created ('lock') or
// opened ('unlock'). STAGE_VISUAL picks the progress ring or the plain spinner.
type Props = {
	mode: 'lock' | 'unlock';
	phase: StagePhase;
	/** Micro caption above the visual, e.g. "preparing your link". */
	caption: string;
	/** The status sentence under the visual; on 'failing' it carries the error. */
	status: string;
	/** 0..1 measured progress (file upload), or null when the step has none. */
	progress?: number | null;
	/** Cancel is offered only while the encrypted PUT can still be aborted. */
	cancellable?: boolean;
	onCancel?: () => void;
};

let {
	mode,
	phase,
	caption,
	status,
	progress = null,
	cancellable = false,
	onCancel
}: Props = $props();
</script>

<section
	class="grid gap-5 rounded-2xl border border-border bg-card p-6 shadow-sm"
	aria-label={mode === 'lock' ? 'Creating your link' : 'Opening your secret'}
>
	<p class="micro text-muted-foreground">{caption}</p>
	<div class="grid place-items-center gap-6 py-6">
		{#if STAGE_VISUAL === 'ring'}
			<ProgressRingLock {mode} {phase} {progress} />
		{:else if phase === 'finishing'}
			<CheckIcon class="size-8 text-success animate-in fade-in zoom-in-50 duration-200" aria-hidden="true" />
		{:else if phase === 'failing'}
			<XIcon class="size-8 text-destructive animate-in fade-in zoom-in-50 duration-200" aria-hidden="true" />
		{:else}
			<LoaderCircleIcon class="size-8 animate-spin text-primary motion-reduce:animate-none" aria-hidden="true" />
		{/if}
		<!-- A failure is announced as an alert by the form it returns to, so the failing
		     text is shown here without a live region to avoid announcing it twice. -->
		<p
			role={phase === 'failing' ? undefined : 'status'}
			aria-live={phase === 'failing' ? undefined : 'polite'}
			class={cn('text-center text-sm', phase === 'failing' ? 'text-destructive' : 'text-muted-foreground')}
		>
			{status}
		</p>
	</div>
	{#if cancellable && phase === 'waiting'}
		<Button type="button" variant="outline" class="h-11 w-full" onclick={onCancel}>Cancel</Button>
	{/if}
</section>
