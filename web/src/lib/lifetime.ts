// Countdown helper for a secret's expiry. Extracted from CreateSecretPage so
// the NaN guard is unit-testable: `Math.max(0, NaN) === NaN` in JS, so a raw
// `new Date(bad).getTime()` would surface `NaN` to the countdown UI (#88).
// Returns 0 for empty/unparseable expiresAt or past expiry — never NaN.

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
