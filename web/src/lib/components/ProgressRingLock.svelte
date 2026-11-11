<script lang="ts">
import type { StagePhase } from '#lib/stage-timing.js';

// Progress ring around a padlock (decorative; the stage card owns the status text).
// waiting: with no measurable progress the ring creeps toward 85% and a dot orbits
//   the ring; with progress (0..1) the ring follows it.
// finishing: the ring fills, then the lock closes ('lock') or its shackle lifts
//   ('unlock'), with a small pop and a burst of dots.
// failing: the ring drains in the destructive color and the lock shakes.
// Motion runs on one rAF clock so the waiting loop hands off to the finishing
// beat without a jump. prefers-reduced-motion stops the clock: waiting is a still
// frame and finishing/failing show only their end state.
type Props = {
	mode: 'lock' | 'unlock';
	phase: StagePhase;
	progress?: number | null;
};

let { mode, phase, progress = null }: Props = $props();

let t = $state(0);
let tf = $state(0);
let shownFill = $state(0);
// Fill at the moment the phase left 'waiting'; the finishing/failing curves start there.
let fillAtTurn = $state(0);

const clamp = (x: number) => Math.min(1, Math.max(0, x));
const span = (time: number, start: number, duration: number) => clamp((time - start) / duration);
const easeOutCubic = (x: number) => 1 - (1 - x) ** 3;
const easeOutBack = (x: number) => 1 + 2.4 * (x - 1) ** 3 + 1.4 * (x - 1) ** 2;
const mix = (a: number, b: number, x: number) => a + (b - a) * x;
// The end of the work is unknown, so the creep only approaches 85%; 100% is the finish.
const creepAt = (time: number) => 0.85 * (1 - Math.exp(-time / 2200));
const waitingTarget = (time: number) =>
	progress === null ? creepAt(time) : 0.05 + 0.9 * clamp(progress);

$effect(() => {
	if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
		const endFill = phase === 'failing' ? 0 : 1;
		tf = phase === 'waiting' ? 0 : 10_000;
		fillAtTurn = endFill;
		shownFill = phase === 'waiting' ? waitingTarget(0) : endFill;
		return;
	}
	let frame = 0;
	const start = performance.now();
	let last = start;
	let turnedAt: number | null = null;
	const loop = (now: number) => {
		if (phase !== 'waiting' && turnedAt === null) {
			turnedAt = now;
			fillAtTurn = shownFill;
		}
		if (turnedAt === null) {
			t = now - start;
			// Ease toward the target so a jump in measured progress still animates.
			shownFill += (waitingTarget(t) - shownFill) * Math.min(1, (now - last) / 180);
		} else {
			tf = now - turnedAt;
		}
		last = now;
		frame = requestAnimationFrame(loop);
	};
	frame = requestAnimationFrame(loop);
	return () => cancelAnimationFrame(frame);
});

const finishing = $derived(phase === 'finishing');
const failing = $derived(phase === 'failing');
const fill = $derived(
	finishing
		? mix(fillAtTurn, 1, easeOutCubic(span(tf, 0, 380)))
		: failing
			? mix(fillAtTurn, 0, easeOutCubic(span(tf, 0, 450)))
			: shownFill
);
// The lock turns once the ring is full (from 320ms).
const turn = $derived(finishing ? easeOutBack(span(tf, 320, 260)) : 0);
const pop = $derived(finishing ? Math.sin(Math.PI * span(tf, 320, 380)) : 0);
const burst = $derived(finishing ? span(tf, 340, 520) : 0);
const shake = $derived(failing ? 2.2 * Math.sin(tf / 38) * Math.exp(-tf / 220) : 0);
const orbit = $derived((t / 12) % 360);
// lock: the raised shackle drops home. unlock: the shackle lifts and swings its left leg out.
const shackle = $derived(
	mode === 'lock'
		? `translateY(${-3.5 * (1 - turn)}px)`
		: `translateY(${-3.5 * turn}px) rotate(${28 * turn}deg)`
);
const BURST_DOTS = Array.from({ length: 10 }, (_, i) => i);
</script>

<svg
	class="size-32"
	viewBox="0 0 120 120"
	fill="none"
	aria-hidden="true"
	style="transform: scale({1 + 0.08 * pop})"
>
	<circle cx="60" cy="60" r="48" style="stroke: var(--border); stroke-width: 4px" />
	<circle
		cx="60"
		cy="60"
		r="48"
		pathLength="100"
		stroke-linecap="round"
		style="stroke: {failing ? 'var(--destructive)' : 'var(--primary)'}; transition: stroke 200ms; stroke-width: 4px; stroke-dasharray: 100; stroke-dashoffset: {100 - fill * 100}; transform: rotate(-90deg); transform-origin: 60px 60px"
	/>
	{#if phase === 'waiting'}
		<!-- The orbiting dot keeps a slow fill from reading as frozen. -->
		<circle
			cx="60"
			cy="12"
			r="2.6"
			style="fill: var(--primary); opacity: 0.55; transform: rotate({orbit}deg); transform-origin: 60px 60px"
		/>
	{/if}
	{#each BURST_DOTS as dot (dot)}
		{@const angle = (dot * 36 * Math.PI) / 180}
		{@const radius = mix(52, 70, easeOutCubic(burst))}
		<circle
			cx={60 + Math.cos(angle) * radius}
			cy={60 + Math.sin(angle) * radius}
			r={dot % 2 ? 2 : 2.8}
			style="fill: var(--primary); opacity: {burst > 0 ? 1 - burst : 0}"
		/>
	{/each}
	<!-- The lucide lock shape (24 units) at 2x; the shackle pivots on its right leg (17,11). -->
	<g
		transform="translate({36 + shake * 2} 34) scale(2)"
		class="transition-colors duration-200 {failing
			? 'text-destructive'
			: turn > 0
				? 'text-primary'
				: 'text-muted-foreground'}"
		stroke="currentColor"
		stroke-width="1.6"
		stroke-linecap="round"
	>
		<path d="M7 11V7a5 5 0 0 1 10 0v4" style="transform: {shackle}; transform-origin: 17px 11px" />
		<rect x="3" y="11" width="18" height="11" rx="2.5" />
		<circle cx="12" cy="15.6" r="1.3" fill="currentColor" stroke="none" />
		<path d="M12 16.4v2" />
	</g>
</svg>
