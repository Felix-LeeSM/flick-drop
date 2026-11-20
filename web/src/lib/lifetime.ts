import { t } from '#lib/i18n/index.js';

// Never NaN: `Math.max(0, NaN)` is NaN, so bad expiresAt is guarded explicitly.
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
