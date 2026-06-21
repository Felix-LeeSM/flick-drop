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

let copied = $state(false);
let resetTimer: ReturnType<typeof setTimeout> | undefined;

async function copy(): Promise<void> {
	if (value.length === 0) {
		return;
	}
	try {
		await navigator.clipboard.writeText(value);
		copied = true;
		clearTimeout(resetTimer);
		resetTimer = setTimeout(() => {
			copied = false;
		}, 1600);
	} catch {
		// Clipboard may be unavailable; the value stays selectable in the input.
	}
}
</script>

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
			copied && 'bg-success/15 text-success'
		)}
		aria-label="Copy to clipboard"
		title="Copy"
		onclick={() => {
			void copy();
		}}
	>
		<CopyIcon class="size-4" />
	</Button>
</div>
