import { t } from '#lib/i18n/index.js';

// Build-time `PUBLIC_FLICK_*` bounds; defaults must equal `internal/config/defaults.go`
// (`scripts/ci/ttl-drift.sh` enforces it).

const DEFAULT_MIN_TTL_SECONDS = 300;
const DEFAULT_MAX_TTL_SECONDS = 604_800;
const DEFAULT_DEFAULT_TTL_SECONDS = 3600;

function positiveInt(raw: unknown, fallback: number): number {
	const value = Number(raw);
	return Number.isFinite(value) && value > 0 ? value : fallback;
}

export const MIN_TTL_SECONDS = positiveInt(
	import.meta.env.PUBLIC_FLICK_MIN_TTL_SECONDS,
	DEFAULT_MIN_TTL_SECONDS
);
export const MAX_TTL_SECONDS = positiveInt(
	import.meta.env.PUBLIC_FLICK_MAX_TTL_SECONDS,
	DEFAULT_MAX_TTL_SECONDS
);
export const DEFAULT_TTL_SECONDS = positiveInt(
	import.meta.env.PUBLIC_FLICK_DEFAULT_TTL_SECONDS,
	DEFAULT_DEFAULT_TTL_SECONDS
);

function formatDuration(seconds: number): string {
	const words = t().common.lifetime;
	if (seconds > 0 && seconds % 86_400 === 0) {
		return words.days(seconds / 86_400);
	}
	if (seconds > 0 && seconds % 3600 === 0) {
		return words.hours(seconds / 3600);
	}
	return words.minutes(Math.round(seconds / 60));
}

export function formatTtlRange(minSeconds: number, maxSeconds: number): string {
	return t().common.lifetime.range(formatDuration(minSeconds), formatDuration(maxSeconds));
}
