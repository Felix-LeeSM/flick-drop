// Countdown helper for a secret's expiry. Extracted from CreateSecretPage so
// the NaN guard is unit-testable: `Math.max(0, NaN) === NaN` in JS, so a raw
// `new Date(bad).getTime()` would surface `NaN` to the countdown UI (#88).
// Returns 0 for empty/unparseable expiresAt or past expiry — never NaN.

import { t } from '#lib/i18n/index.js';

export function remainingSecondsFrom(expiresAt: string, nowMs: number): number {
	if (!expiresAt) {
		return 0;
	}
	const expiresMs = Date.parse(expiresAt);
	if (!Number.isFinite(expiresMs)) {
		return 0;
	}
	return Math.max(0, Math.round((expiresMs - nowMs) / 1000));
}

// Countdown label shared by delivery and request pages: two largest units, so a
// seven-day lifetime reads "6d 23h" rather than "10079m 59s".
export function formatRemaining(seconds: number): string {
	const words = t().common.lifetime;
	if (seconds >= 86_400) {
		return words.daysHours(Math.floor(seconds / 86_400), Math.floor((seconds % 86_400) / 3600));
	}
	if (seconds >= 3600) {
		return words.hoursMinutes(Math.floor(seconds / 3600), Math.floor((seconds % 3600) / 60));
	}
	return words.minutesSeconds(Math.floor(seconds / 60), seconds % 60);
}
