import { get, writable } from 'svelte/store';
import { browser } from '$app/environment';

export type ThemePreference = 'system' | 'light' | 'dark';
export type ResolvedTheme = 'light' | 'dark';

const themeStorageKey = 'burnlink-theme';

const preference = writable<ThemePreference>('system');
const resolved = writable<ResolvedTheme>('light');

let initialized = false;
let systemScheme: MediaQueryList | null = null;

export const themePreference = {
	subscribe: preference.subscribe
};

export const resolvedTheme = {
	subscribe: resolved.subscribe
};

export function initializeTheme(): void {
	if (!browser || initialized) {
		return;
	}

	initialized = true;
	systemScheme = window.matchMedia('(prefers-color-scheme: dark)');
	preference.set(readStoredPreference());
	preference.subscribe(applyTheme);

	systemScheme.addEventListener('change', () => {
		if (get(preference) === 'system') {
			applyTheme('system');
		}
	});
}

export function setThemePreference(nextPreference: ThemePreference): void {
	if (!browser) {
		return;
	}

	try {
		if (nextPreference === 'system') {
			window.localStorage.removeItem(themeStorageKey);
		} else {
			window.localStorage.setItem(themeStorageKey, nextPreference);
		}
	} catch {
		// Keep the current tab usable when browser storage is unavailable.
	}

	preference.set(nextPreference);
}

export function toggleResolvedTheme(): void {
	setThemePreference(get(resolved) === 'dark' ? 'light' : 'dark');
}

function readStoredPreference(): ThemePreference {
	try {
		const stored = window.localStorage.getItem(themeStorageKey);
		if (stored === 'light' || stored === 'dark' || stored === 'system') {
			return stored;
		}
	} catch {
		return 'system';
	}
	return 'system';
}

function applyTheme(nextPreference: ThemePreference): void {
	const nextResolved = resolveTheme(nextPreference);
	const root = document.documentElement;

	root.classList.toggle('dark', nextResolved === 'dark');
	root.dataset.theme = nextResolved;
	root.style.colorScheme = nextResolved;
	resolved.set(nextResolved);
}

function resolveTheme(nextPreference: ThemePreference): ResolvedTheme {
	if (nextPreference === 'dark') {
		return 'dark';
	}
	if (nextPreference === 'light') {
		return 'light';
	}
	return systemScheme?.matches ? 'dark' : 'light';
}
