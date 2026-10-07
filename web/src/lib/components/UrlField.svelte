<script lang="ts">
import { CopyIcon } from '@lucide/svelte';
import { Button } from '$lib/components/ui/button';
import { Input } from '$lib/components/ui/input';
import { cn } from '$lib/utils.js';

type Props = {
	value: string;
	id?: string;
	class?: string;
};

let { value, id, class: className = '' }: Props = $props();

let copyState = $state<'idle' | 'copied' | 'failed'>('idle');
let resetTimer: ReturnType<typeof setTimeout> | undefined;

async function copy(): Promise<void> {
	if (value.length === 0) {
		return;
	}
	clearTimeout(resetTimer);
	try {
		await navigator.clipboard.writeText(value);
		copyState = 'copied';
		resetTimer = setTimeout(() => {
			copyState = 'idle';
		}, 2400);
	} catch {
		// Clipboard may be unavailable (insecure context, denied permission). Say so
		// instead of failing silently; the value stays selectable in the input. The
		// failure text is not auto-cleared because it carries an instruction.
		copyState = 'failed';
	}
}
</script>

<div class="grid w-full gap-1.5">
	<div class={cn('relative flex items-center', className)}>
		<Input
			{id}
			class="h-11 w-full truncate pr-12 font-mono text-sm"
			value={value}
			readonly
			aria-label="Share URL"
			title={value}
		/>
		<Button
			type="button"
			variant="ghost"
			size="icon"
			class={cn(
				'absolute right-1 size-9 text-muted-foreground hover:text-foreground',
				copyState === 'copied' && 'bg-success/15 text-success'
			)}
			aria-label="Copy to clipboard"
			title="Copy"
			onclick={() => {
				void copy();
			}}
		>
			<CopyIcon class="size-4" aria-hidden="true" />
		</Button>
	</div>
	<p
		class="min-h-4 text-xs"
		class:text-success={copyState === 'copied'}
		class:text-destructive={copyState === 'failed'}
		role="status"
		aria-live="polite"
	>
		{#if copyState === 'copied'}
			Copied
		{:else if copyState === 'failed'}
			Copy unavailable. Select the link above and copy it.
		{/if}
	</p>
</div>
