<script lang="ts">
import './layout.css';
import favicon from '$lib/assets/favicon.svg';
import { initializeTheme } from '$lib/state/theme';

let { children } = $props();

initializeTheme();
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<meta name="color-scheme" content="light dark" />
	<script>
		(() => {
			try {
				const stored = window.localStorage.getItem('burnlink-theme');
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
