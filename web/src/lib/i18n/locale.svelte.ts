// URLs never carry the language: a shared link opens in the recipient's own.
export const LOCALES = ['en', 'ko', 'ja'] as const;
export type Locale = (typeof LOCALES)[number];

export const LOCALE_NAMES: Record<Locale, string> = { en: 'English', ko: '한국어', ja: '日本語' };

export const LOCALE_STORAGE_KEY = 'flick-locale';

let current = $state<Locale>('en');

export function getLocale(): Locale {
	return current;
}

export function isLocale(value: unknown): value is Locale {
	return typeof value === 'string' && (LOCALES as readonly string[]).includes(value);
}

export function detectLocale(stored: string | null, languages: readonly string[]): Locale {
	if (isLocale(stored)) {
		return stored;
	}
	for (const language of languages) {
		const primary = language.toLowerCase().split('-')[0];
		if (isLocale(primary)) {
			return primary;
		}
	}
	return 'en';
}

function apply(next: Locale): void {
	current = next;
	if (typeof document !== 'undefined') {
		document.documentElement.lang = next;
	}
}

// Runs once in the browser before the first render that reads messages.
export function initializeLocale(): void {
	if (typeof window === 'undefined') {
		return;
	}
	let stored: string | null = null;
	try {
		stored = window.localStorage.getItem(LOCALE_STORAGE_KEY);
	} catch {
		// Storage can be blocked; the browser language still applies.
	}
	apply(detectLocale(stored, navigator.languages ?? [navigator.language]));
}

export function chooseLocale(next: Locale): void {
	apply(next);
	try {
		window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
	} catch {
		// The choice still applies to this page view.
	}
}
