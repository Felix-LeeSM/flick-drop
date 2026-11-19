// The display language. URLs never carry it: a shared link opens in the
// recipient's own language. A manual choice is remembered in this browser;
// otherwise the browser's language list decides, and English is the fallback.
export const LOCALES = ['en', 'ko', 'ja'] as const;
export type Locale = (typeof LOCALES)[number];

// Each language names itself, so a reader finds theirs whatever is showing.
export const LOCALE_NAMES: Record<Locale, string> = { en: 'English', ko: '한국어', ja: '日本語' };

export const LOCALE_STORAGE_KEY = 'flick-locale';

let current = $state<Locale>('en');

export function getLocale(): Locale {
	return current;
}

export function isLocale(value: unknown): value is Locale {
	return typeof value === 'string' && (LOCALES as readonly string[]).includes(value);
}

// A stored manual choice wins; then the first browser language whose primary
// subtag Flick has (ko-KR -> ko); then English.
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

// A manual choice from the language row, remembered for later visits.
export function chooseLocale(next: Locale): void {
	apply(next);
	try {
		window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
	} catch {
		// The choice still applies to this page view.
	}
}
