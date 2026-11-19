<script lang="ts">
import { CheckIcon, LoaderCircleIcon, XIcon } from '@lucide/svelte';
import ProgressRingLock from '#lib/components/ProgressRingLock.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { t } from '#lib/i18n/index.js';
import { STAGE_VISUAL, type StagePhase } from '#lib/stage-timing.js';
import { cn } from '#lib/utils.js';

type Props = {
	mode: 'lock' | 'unlock';
	phase: StagePhase;
	label?: string;
	caption: string;
	/** On 'failing' it carries the error. */
	status: string;
	/** 0..1, or null when the step has no measured progress. */
	progress?: number | null;
	/** Only while the encrypted PUT can still be aborted. */
	cancellable?: boolean;
	onCancel?: () => void;
};

let {
	mode,
	phase,
	label,
	caption,
	status,
	progress = null,
	cancellable = false,
	onCancel
}: Props = $props();
</script>

<section
	class="grid gap-5 rounded-2xl border border-border bg-card p-6 shadow-sm"
	aria-label={label ?? (mode === 'lock' ? t().common.stage.creatingLink : t().common.stage.openingSecret)}
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
		<!-- No live region on failure: the form announces it as an alert. -->
		<p
			role={phase === 'failing' ? undefined : 'status'}
			aria-live={phase === 'failing' ? undefined : 'polite'}
			class={cn('text-center text-sm', phase === 'failing' ? 'text-destructive' : 'text-muted-foreground')}
		>
			{status}
		</p>
	</div>
	{#if cancellable && phase === 'waiting'}
		<Button type="button" variant="outline" class="h-11 w-full" onclick={onCancel}>{t().common.stage.cancel}</Button>
	{/if}
</section>
