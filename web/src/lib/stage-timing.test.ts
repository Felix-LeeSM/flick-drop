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
		let settled = false;
		void holdMinimumStage(0, 400).then(() => {
			settled = true;
		});

		await vi.advanceTimersByTimeAsync(STAGE_MIN_SHOW_MS - 400 - 1);
		expect(settled).toBe(false);

		await vi.advanceTimersByTimeAsync(1);
		expect(settled).toBe(true);
	});

	it('adds no wait once the work outlasted the minimum', async () => {
		let settled = false;
		void holdMinimumStage(0, STAGE_MIN_SHOW_MS + 2000).then(() => {
			settled = true;
		});

		await vi.advanceTimersByTimeAsync(0);

		expect(settled).toBe(true);
	});
});
