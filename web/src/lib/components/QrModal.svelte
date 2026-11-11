<script lang="ts">
import { XIcon } from '@lucide/svelte';
import { Dialog } from 'bits-ui';
import QrCode from '#lib/components/QrCode.svelte';
import UrlField from '#lib/components/UrlField.svelte';
import { buttonVariants } from '#lib/components/ui/button/index.js';
import { cn } from '#lib/utils.js';

type Props = {
	open?: boolean;
	url: string;
};

let { open = $bindable(false), url }: Props = $props();
</script>

<Dialog.Root bind:open={open}>
	<Dialog.Portal>
		<Dialog.Overlay
			class="fixed inset-0 z-50 bg-black/55 backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0"
		/>
		<Dialog.Content
			class="fixed left-1/2 top-1/2 z-50 grid w-[27rem] max-w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border bg-card p-7 pb-7 text-center text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<div class="relative">
				<Dialog.Close
					class="absolute -right-2.5 -top-2.5 grid size-8 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
					aria-label="Close"
				>
					<XIcon class="size-4" aria-hidden="true" />
				</Dialog.Close>
				<Dialog.Title class="mt-0.5 font-serif text-2xl">Scan or copy</Dialog.Title>
			</div>
			<Dialog.Description class="text-sm text-muted-foreground">
				Point a camera at the code, or copy the link below.
			</Dialog.Description>
			<QrCode value={url} class="mx-auto size-52 rounded-lg bg-white p-2.5 shadow-xs" />
			<UrlField value={url} class="h-12" />
			<Dialog.Close class={cn(buttonVariants({ variant: 'outline' }), 'h-12 w-full')}>
				Close
			</Dialog.Close>
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
