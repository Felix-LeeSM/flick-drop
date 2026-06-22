<script lang="ts">
import { MoonIcon, SunIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import { Button } from '$lib/components/ui/button';
import { initializeTheme, resolvedTheme, toggleResolvedTheme } from '$lib/state/theme';

let isMounted = $state(false);
let isDark = $state(false);

onMount(() => {
	initializeTheme();
	isMounted = true;

	const unsubscribe = resolvedTheme.subscribe((theme) => {
		isDark = theme === 'dark';
	});

	return unsubscribe;
});
</script>

<Button
	type="button"
	variant="outline"
	size="icon-sm"
	aria-label={isDark ? 'Use light theme' : 'Use dark theme'}
	title={isDark ? 'Use light theme' : 'Use dark theme'}
	onclick={toggleResolvedTheme}
>
	{#if isMounted && isDark}
		<SunIcon class="size-4" aria-hidden="true" />
	{:else}
		<MoonIcon class="size-4" aria-hidden="true" />
	{/if}
</Button>
