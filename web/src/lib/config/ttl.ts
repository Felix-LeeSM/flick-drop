// TTL lifetime bounds, mirrored at build time from the server config
// (`internal/config/defaults.go`). The web client is adapter-static, so these
// reach the browser via `PUBLIC_FLICK_*` env at build (see web/Dockerfile and
// .github/workflows/publish-images.yml); there is no runtime config endpoint.
// The defaults below must equal the Go constants — `scripts/ci/ttl-drift.sh`
// fails the build if they drift.

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

// Format a TTL bound as the largest whole unit (days > hours > minutes), so the
// validation message reads naturally for the server-configured range.
function formatDuration(seconds: number): string {
	if (seconds > 0 && seconds % 86_400 === 0) {
		const days = seconds / 86_400;
		return `${days} day${days === 1 ? '' : 's'}`;
	}
	if (seconds > 0 && seconds % 3600 === 0) {
		const hours = seconds / 3600;
		return `${hours} hour${hours === 1 ? '' : 's'}`;
	}
	const minutes = Math.round(seconds / 60);
	return `${minutes} minute${minutes === 1 ? '' : 's'}`;
}

export function formatTtlRange(minSeconds: number, maxSeconds: number): string {
	return `Choose a lifetime between ${formatDuration(minSeconds)} and ${formatDuration(maxSeconds)}.`;
}
