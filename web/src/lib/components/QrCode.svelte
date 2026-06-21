<script lang="ts">
import { renderSVG } from 'uqr';
import { cn } from '$lib/utils.js';

type Props = {
	value: string;
	class?: string;
};

let { value, class: className = '' }: Props = $props();

// White canvas + black modules stays high-contrast for scanners in both themes.
const svg = $derived(
	value.length > 0
		? renderSVG(value, {
				ecc: 'M',
				pixelSize: 10,
				border: 2,
				whiteColor: '#ffffff',
				blackColor: '#000000'
			})
		: ''
);
</script>

<div
	role="img"
	aria-label="QR code for the secret share link"
	class={cn('qr grid place-items-center overflow-hidden', className)}
>
	{#if svg.length > 0}
		<!-- eslint-disable-next-line svelte/no-at-html-tags -- uqr renders a static, trusted SVG from local input. -->
		{@html svg}
	{/if}
</div>

<style>
	.qr :global(svg) {
		display: block;
		width: 100%;
		height: 100%;
	}
</style>
