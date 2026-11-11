<script lang="ts">
import { Dialog } from 'bits-ui';
import { buttonVariants } from '#lib/components/ui/button/index.js';
import { cn } from '#lib/utils.js';

type Props = {
	open?: boolean;
	title: string;
	description: string;
	confirmLabel: string;
	busy?: boolean;
	onconfirm?: () => void;
};

let { open = $bindable(false), title, description, confirmLabel, busy = false, onconfirm }: Props = $props();
</script>

<Dialog.Root bind:open>
	<Dialog.Portal>
		<Dialog.Overlay
			class="fixed inset-0 z-50 bg-black/55 backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0"
		/>
		<Dialog.Content
			class="fixed left-1/2 top-1/2 z-50 grid w-[20rem] max-w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border bg-card p-5 pb-6 text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<Dialog.Title class="font-serif text-xl">{title}</Dialog.Title>
			<Dialog.Description class="text-sm leading-[1.55] text-muted-foreground">{description}</Dialog.Description>
			<div class="grid grid-cols-2 gap-2">
				<Dialog.Close class={cn(buttonVariants({ variant: 'outline' }), 'h-10 rounded-[10px]')} disabled={busy}>Keep it</Dialog.Close>
				<button
					class={cn(buttonVariants({ variant: 'destructive' }), 'h-10 rounded-[10px]')}
					disabled={busy}
					onclick={() => {
						open = false;
						onconfirm?.();
					}}
				>{confirmLabel}</button>
			</div>
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
