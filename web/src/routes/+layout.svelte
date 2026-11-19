<script lang="ts">
import './layout.css';
import favicon from '#lib/assets/favicon.svg';
import { initializeLocale } from '#lib/i18n/locale.svelte.js';
import { initializeTheme } from '#lib/state/theme.js';

let { children } = $props();

initializeTheme();
// Before any page mounts: a child's onMount runs before this layout's and would capture English.
initializeLocale();
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
