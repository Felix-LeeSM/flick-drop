export const STAGE_VISUAL: 'ring' | 'spin' = 'ring';

// Keeps a fast create/open from flashing the stage card; one ring fill reads.
export const STAGE_MIN_SHOW_MS = 1500;
export const STAGE_FINISH_MS = 1000;
export const STAGE_FAIL_MS = 900;

export type StagePhase = 'waiting' | 'finishing' | 'failing';

export function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

export function holdMinimumStage(startedAt: number, now = performance.now()): Promise<void> {
	return sleep(Math.max(0, STAGE_MIN_SHOW_MS - (now - startedAt)));
}
