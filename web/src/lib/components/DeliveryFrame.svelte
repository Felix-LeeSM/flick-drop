<script lang="ts">
import type { Snippet } from 'svelte';
import type { ReceivedPipePayload } from '#lib/components/PipeReceivedSecretView.svelte';
import SiteHeader, { type HeaderLink } from '#lib/components/SiteHeader.svelte';

let {
	children,
	link,
	showEnterCode = false,
	receiveModalOpen = $bindable(false),
	onCodeReceived
}: {
	children: Snippet;
	link?: HeaderLink;
	showEnterCode?: boolean;
	receiveModalOpen?: boolean;
	onCodeReceived?: (payload: ReceivedPipePayload) => void;
} = $props();
</script>

<main class="min-h-screen px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl grid-cols-[minmax(0,1fr)] gap-8">
		<SiteHeader
			{link}
			{showEnterCode}
			bind:receiveModalOpen
			{onCodeReceived}
		/>
		<div class="grid grid-cols-[minmax(0,1fr)] gap-7">{@render children()}</div>
	</div>
</main>
