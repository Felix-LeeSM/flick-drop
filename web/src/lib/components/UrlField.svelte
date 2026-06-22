<script lang="ts">
import { CopyIcon } from '@lucide/svelte';
import { Button } from '$lib/components/ui/button';
import { cn } from '$lib/utils.js';

type Props = {
	value: string;
	id?: string;
	class?: string;
};

let { value, id, class: className = '' }: Props = $props();

let copied = $state(false);
let field: HTMLDivElement;
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
		// Clipboard may be unavailable; the value stays selectable in the field.
	}
}

// Click selects the whole URL so it can be copied without the share button.
function selectAll(): void {
	const selection = window.getSelection();
	if (!selection || !field) {
		return;
	}
	const range = document.createRange();
	range.selectNodeContents(field);
	selection.removeAllRanges();
	selection.addRange(range);
}
</script>

<div class={cn('relative flex items-center', className)}>
	<!-- A readonly <input> can't ellipsize its value (text-overflow does not apply
	     to an input's value), so the URL is rendered in a truncating span instead.
	     The wrapper keeps the Input look (border / bg / shadow) and stays focusable. -->
	<div
		bind:this={field}
		{id}
		class="dark:bg-input/30 border-input focus-visible:border-ring focus-visible:ring-ring/50 flex h-11 w-full cursor-text items-center overflow-hidden rounded-md border bg-transparent pr-12 font-mono text-sm shadow-xs outline-none focus-visible:ring-3"
		role="textbox"
		aria-label="Share URL"
		aria-readonly="true"
		tabindex={0}
		title={value}
		onclick={selectAll}
		onkeydown={(event) => {
			if (event.key === 'Enter' || event.key === ' ') {
				event.preventDefault();
				selectAll();
			}
		}}
	>
		<span class="min-w-0 flex-1 truncate px-2.5 py-1">{value}</span>
	</div>
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
