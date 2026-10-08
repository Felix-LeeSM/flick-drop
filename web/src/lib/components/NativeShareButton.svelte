<script lang="ts">
import { Share2Icon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { Button } from '$lib/components/ui/button';

let { recipientUrl }: { recipientUrl: string } = $props();
let supported = $state(false);
let sharing = $state(false);
let failed = $state(false);

onMount(() => {
	supported = typeof navigator.share === 'function';
});

// An explicit recipient URL keeps a future management-page URL out of sharing.
const shareData = $derived({ title: 'Flick', url: recipientUrl });
const canShare = $derived(
	supported &&
		recipientUrl.length > 0 &&
		(typeof navigator.canShare !== 'function' || navigator.canShare(shareData))
);

async function share(): Promise<void> {
	if (!canShare || sharing) {
		return;
	}
	sharing = true;
	failed = false;
	try {
		await navigator.share(shareData);
	} catch (error) {
		// AbortError also covers no available share target; copy and QR still work.
		failed = !(error instanceof DOMException && error.name === 'AbortError');
	} finally {
		sharing = false;
	}
}
</script>

{#if canShare}
	<div class="grid gap-1.5">
		<Button type="button" variant="outline" class="h-11 w-full" disabled={sharing} onclick={share}>
			<Share2Icon class="size-4" aria-hidden="true" />
			Share link
		</Button>
		{#if failed}
			<p class="text-sm text-destructive" role="alert">
				Could not share. Copy the link or use its QR code.
			</p>
		{/if}
	</div>
{/if}
