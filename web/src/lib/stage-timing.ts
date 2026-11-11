// Pacing for the lock stage card shown while a secret is created or opened.
// A fast create or open would otherwise flash the card for a few hundred
// milliseconds, so the waiting loop stays up for at least STAGE_MIN_SHOW_MS and
// the lock/unlock (or failure) beat plays in full before the page moves on.

/** Visual inside the stage card. 'spin' is the plain spinner alternative. */
export const STAGE_VISUAL: 'ring' | 'spin' = 'ring';

/** Minimum time the waiting loop stays visible, so one fill of the ring reads. */
export const STAGE_MIN_SHOW_MS = 1500;
/** Ring completes, the lock closes or opens, then holds before the next view. */
export const STAGE_FINISH_MS = 1000;
/** Ring drains and the lock shakes before the form returns with its error. */
export const STAGE_FAIL_MS = 900;

export type StagePhase = 'waiting' | 'finishing' | 'failing';

export function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Waits out whatever is left of STAGE_MIN_SHOW_MS since the stage started. */
export function holdMinimumStage(startedAt: number, now = performance.now()): Promise<void> {
	return sleep(Math.max(0, STAGE_MIN_SHOW_MS - (now - startedAt)));
}
