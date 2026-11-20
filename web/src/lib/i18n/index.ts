import { getLocale, type Locale } from './locale.svelte';
import { en, type Messages } from './messages/en';
import { ja } from './messages/ja';
import { ko } from './messages/ko';

const catalogs: Record<Locale, Messages> = { en, ko, ja };

// Read inside markup or $derived so text re-renders on language change.
export function t(): Messages {
	return catalogs[getLocale()];
}

export type { Messages };
