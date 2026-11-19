<script lang="ts">
import { CopyIcon } from '@lucide/svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Input } from '#lib/components/ui/input/index.js';
import { t } from '#lib/i18n/index.js';
import { cn } from '#lib/utils.js';

type Props = {
	value: string;
	id?: string;
	class?: string;
	label?: string;
	copyLabel?: string;
	prominent?: boolean;
	oncopied?: () => void;
};

let {
	value,
	id,
	class: className = '',
	label,
	copyLabel,
	prominent = false,
	oncopied
}: Props = $props();

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
		oncopied?.();
		resetTimer = setTimeout(() => {
			copyState = 'idle';
		}, 2400);
	} catch {
		// Clipboard may be unavailable; the failure text is not auto-cleared because it carries an instruction.
		copyState = 'failed';
	}
}
</script>

<div class="grid w-full gap-1.5">
	<div class={cn('relative flex items-center', className)}>
		<Input
			{id}
			class={cn('h-12 w-full truncate pr-12 font-mono text-sm', prominent && 'pr-24')}
			value={value}
			readonly
			aria-label={label ?? t().common.urlField.label}
			title={value}
		/>
		<Button
			type="button"
			variant={prominent ? 'default' : 'ghost'}
			size={prominent ? 'sm' : 'icon'}
			class={cn(
				prominent
					? 'absolute right-1.5 h-9 gap-1.5 rounded-lg px-3 text-[0.8125rem]'
					: 'absolute right-1.5 size-9 text-muted-foreground hover:text-foreground',
				!prominent && copyState === 'copied' && 'bg-success/15 text-success'
			)}
			aria-label={copyLabel ?? t().common.urlField.copyToClipboard}
			title={t().common.urlField.copy}
			onclick={() => {
				void copy();
			}}
		>
			<CopyIcon class="size-4" aria-hidden="true" />
			{#if prominent}{t().common.urlField.copy}{/if}
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
			{t().common.urlField.copied}
		{:else if copyState === 'failed'}
			{t().common.urlField.copyFailed}
		{/if}
	</p>
</div>
