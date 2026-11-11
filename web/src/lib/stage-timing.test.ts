import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { holdMinimumStage, STAGE_MIN_SHOW_MS } from '#lib/stage-timing.js';

describe('holdMinimumStage', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('waits out the rest of the minimum when the work finished early', async () => {
		// Given: the stage started 400ms ago
		let settled = false;
		void holdMinimumStage(0, 400).then(() => {
			settled = true;
		});

		// When: just short of the remaining time passes
		await vi.advanceTimersByTimeAsync(STAGE_MIN_SHOW_MS - 400 - 1);
		// Then: still holding
		expect(settled).toBe(false);

		// When: the remaining time is reached
		await vi.advanceTimersByTimeAsync(1);
		// Then: the hold ends
		expect(settled).toBe(true);
	});

	it('adds no wait once the work outlasted the minimum', async () => {
		// Given: the work already ran past the minimum
		let settled = false;
		void holdMinimumStage(0, STAGE_MIN_SHOW_MS + 2000).then(() => {
			settled = true;
		});

		// When: only the zero-delay timer runs
		await vi.advanceTimersByTimeAsync(0);

		// Then: the hold is already over
		expect(settled).toBe(true);
	});
});
