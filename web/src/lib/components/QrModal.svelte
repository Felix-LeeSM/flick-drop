<script lang="ts">
import { XIcon } from '@lucide/svelte';
import { Dialog } from 'bits-ui';
import QrCode from '$lib/components/QrCode.svelte';
import UrlField from '$lib/components/UrlField.svelte';
import { buttonVariants } from '$lib/components/ui/button';
import { cn } from '$lib/utils.js';

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
			class="fixed left-1/2 top-1/2 z-50 grid w-[18rem] max-w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border bg-card p-5 pb-6 text-center text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<div class="relative">
				<Dialog.Close
					class="absolute -right-2 -top-2 grid size-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
					aria-label="Close"
				>
					<XIcon class="size-4" />
				</Dialog.Close>
				<Dialog.Title class="mt-0.5 font-serif text-xl">Scan or copy</Dialog.Title>
			</div>
			<Dialog.Description class="sr-only">
				Scan the QR code or copy the link to share the secret.
			</Dialog.Description>
			<QrCode value={url} class="mx-auto size-36 rounded-lg bg-white p-1.5 shadow-xs" />
			<UrlField value={url} class="h-12" />
			<Dialog.Close class={cn(buttonVariants({ variant: 'outline' }), 'h-12 w-full')}>
				Close
			</Dialog.Close>
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
