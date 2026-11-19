import { getLocale, type Locale } from './locale.svelte';
import { en, type Messages } from './messages/en';
import { ja } from './messages/ja';
import { ko } from './messages/ko';

const catalogs: Record<Locale, Messages> = { en, ko, ja };

// The catalog of the current language. Reading it inside markup or $derived
// re-renders the text when the language changes.
export function t(): Messages {
	return catalogs[getLocale()];
}

export type { Messages };
