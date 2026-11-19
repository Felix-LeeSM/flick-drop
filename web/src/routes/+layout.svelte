<script lang="ts">
import './layout.css';
import { onMount } from 'svelte';
import favicon from '#lib/assets/favicon.svg';
import LanguageRow from '#lib/components/LanguageRow.svelte';
import { initializeLocale } from '#lib/i18n/locale.svelte.js';
import { initializeTheme } from '#lib/state/theme.js';

let { children } = $props();

initializeTheme();
// After hydration: prerendered pages ship English markup, and switching the language
// before hydration matches it would leave stale text behind.
onMount(initializeLocale);
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<meta name="color-scheme" content="light dark" />
	<script>
		(() => {
			try {
				const stored = window.localStorage.getItem('flick-theme');
				const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
				const isDark = stored === 'dark' || (stored !== 'light' && prefersDark);
				document.documentElement.classList.toggle('dark', isDark);
				document.documentElement.dataset.theme = isDark ? 'dark' : 'light';
				document.documentElement.style.colorScheme = isDark ? 'dark' : 'light';
			} catch {
				// Keep the server-rendered light theme if storage or media queries are unavailable.
			}
		})();
	</script>
</svelte:head>

{@render children()}
<LanguageRow />
