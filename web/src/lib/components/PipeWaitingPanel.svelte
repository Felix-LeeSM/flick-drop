<script lang="ts">
import { CheckIcon, CopyIcon, QrCodeIcon } from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import LockStageCard from '#lib/components/LockStageCard.svelte';
import PipePendingList from '#lib/components/PipePendingList.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { formatPipeCode } from '#lib/crypto/pipe.js';
import {
	holdMinimumStage,
	STAGE_FAIL_MS,
	STAGE_FINISH_MS,
	type StagePhase,
	sleep
} from '#lib/stage-timing.js';
import type { PendingReceiver } from '#lib/state/pipe-relay.svelte.js';

type Props = {
	code: string;
	expiresInSeconds?: number;
	onCancel: () => void;
	onReset: () => void;
	statusText?: string;
	progressPercent?: number;
	completed?: boolean;
	failed?: boolean;
	pending?: PendingReceiver[];
	onSend?: (connectionId: string) => void;
	onDeny?: (connectionId: string) => void;
};

// Mirrors MaxPendingConnections in internal/piperelay/coordinator.go.
const PIPE_ROOM_CAPACITY = 10;

let {
	code,
	expiresInSeconds = 180,
	onCancel,
	onReset,
	statusText = 'Waiting for receiver...',
	progressPercent = 0,
	completed = false,
	failed = false,
	pending = [],
	onSend = () => undefined,
	onDeny = () => undefined
}: Props = $props();

let remainingSeconds = $state(180);
// Lock stage: once bytes start streaming the code view gives way to the progress
// ring, which keeps its minimum and plays the lock (or failure) beat before the
// completed view or the error card shows. The relay reports 25/50/100.
let streamStartedAt: number | null = null;
let stagePhase = $state<StagePhase>('waiting');
let stageSettled = $state(false);
const streaming = $derived(progressPercent > 0);
const showStage = $derived(streaming && !stageSettled);

$effect(() => {
	if (streaming && streamStartedAt === null) {
		streamStartedAt = performance.now();
	}
});

$effect(() => {
	if (!streaming || !(completed || failed)) {
		return;
	}
	const startedAt = streamStartedAt ?? performance.now();
	const ending = completed ? 'finishing' : 'failing';
	let current = true;
	void (async () => {
		await holdMinimumStage(startedAt);
		if (!current) {
			return;
		}
		stagePhase = ending;
		await sleep(ending === 'finishing' ? STAGE_FINISH_MS : STAGE_FAIL_MS);
		if (current) {
			stageSettled = true;
		}
	})();
	return () => {
		current = false;
	};
});
let qrOpen = $state(false);
let copied = $state(false);

let timerInterval: number | null = null;
// Drives the waiting list's "12s ago" labels.
let now = $state(Date.now());

onMount(() => {
	remainingSeconds = expiresInSeconds;
	timerInterval = window.setInterval(() => {
		now = Date.now();
		if (remainingSeconds > 0 && !completed) {
			remainingSeconds -= 1;
		}
	}, 1000);
});

onDestroy(() => {
	if (timerInterval !== null) {
		clearInterval(timerInterval);
	}
});

function formatTime(sec: number): string {
	const safe = Math.max(0, sec);
	const mins = Math.floor(safe / 60);
	const secs = safe % 60;
	return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
}

async function copyCode(): Promise<void> {
	try {
		await navigator.clipboard.writeText(code);
		copied = true;
		setTimeout(() => {
			copied = false;
		}, 2000);
	} catch {
		// Fallback ignored
	}
}

const formattedCode = $derived(formatPipeCode(code));
const firstHalf = $derived(formattedCode.slice(0, 3));
const secondHalf = $derived(formattedCode.slice(4));

const NORMAL_STATUS_MESSAGES = new Set([
	'',
	'Waiting for receiver...',
	'Waiting for receiver to enter code...',
	'Initializing live transfer session...',
	'Sending to the chosen device...',
	'Encrypting stream on-the-fly (age format)...',
	'Streaming encrypted bytes directly to receiver...',
	'Transfer complete!'
]);
const waitingNotice = $derived(NORMAL_STATUS_MESSAGES.has(statusText) ? '' : statusText);

// Direct receive URL that can be encoded in QR code
let directReceiveUrl = $derived(
	typeof window === 'undefined'
		? `https://flick.drop/#code=${code}`
		: `${window.location.origin}/#code=${code}`
);
</script>

<div class="grid gap-4">
	{#if showStage}
		<LockStageCard
			mode="lock"
			phase={stagePhase}
			label="Sending your secret"
			caption="sending to receiver"
			status={stagePhase === 'finishing'
				? 'Cleared from memory'
				: stagePhase === 'failing'
					? statusText
					: progressPercent >= 50
						? 'Streaming to receiver…'
						: 'Encrypting in your browser…'}
			progress={progressPercent / 100}
			cancellable={stagePhase === 'waiting'}
			{onCancel}
		/>
	{:else if completed}
		<!-- The ring already played the lock beat, so the completed view skips a second check. -->
		<section class="grid gap-6">
			<div class="grid gap-1.5">
				<p class="micro flex items-center gap-1.5 text-success">
					<CheckIcon class="size-3.5 shrink-0" aria-hidden="true" />
					transfer complete
				</p>
				<h1 class="page-title">Transfer complete</h1>
				<p class="text-sm text-muted-foreground">
					Delivered directly from this tab with zero server storage.
				</p>
			</div>

			<Button type="button" size="cta" class="w-full" onclick={onReset}>
				Send another secret
			</Button>
		</section>
	{:else if failed && (stageSettled || !streaming)}
		<!-- A broken stream or an ended session (expired, server error) leaves a dead
		     code; the code view would offer a code that no longer works. -->
		<section class="grid gap-6" role="alert">
			<div class="grid gap-1.5">
				<p class="micro text-destructive">session ended</p>
				<h1 class="page-title">Transfer failed</h1>
				<p class="text-sm text-muted-foreground">{statusText}</p>
				<p class="text-xs text-muted-foreground">The code no longer works. Nothing more will be sent.</p>
			</div>
			<Button type="button" size="cta" variant="outline" class="w-full" onclick={onReset}>
				Start new secret
			</Button>
		</section>
	{:else}
		<section class="grid gap-5 rounded-xl border border-border bg-card p-5 shadow-sm" aria-labelledby="pipe-waiting-heading">
			<h1 id="pipe-waiting-heading" class="sr-only">Live transfer</h1>
			<div class="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3.5">
				<div class="flex items-center gap-2">
					<span class="size-2 rounded-full bg-primary animate-pulse motion-reduce:animate-none" aria-hidden="true"></span>
					<span class="micro text-foreground">Live transfer code</span>
				</div>
				<span class="font-mono text-xs tabular-nums text-muted-foreground">
					Expires in <strong class="font-bold text-foreground">{formatTime(remainingSeconds)}</strong>
				</span>
			</div>

			<div class="grid gap-3 py-1 text-center">
				<p class="text-sm text-muted-foreground">
					Recipient clicks <strong class="font-medium text-foreground">Enter Code</strong> in the header and enters:
				</p>
				<div class="flex min-w-0 items-center justify-center gap-2" aria-label="Transfer code {formattedCode}">
					<div class="flex gap-1.5 font-mono text-2xl font-bold sm:text-[1.75rem]">
						{#each firstHalf as digit, idx (`a-${idx}`)}
							<span class="grid h-12 w-9 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs sm:h-14 sm:w-11">
								{digit}
							</span>
						{/each}
					</div>
					<span class="font-mono text-xl text-muted-foreground" aria-hidden="true">-</span>
					<div class="flex gap-1.5 font-mono text-2xl font-bold sm:text-[1.75rem]">
						{#each secondHalf as digit, idx (`b-${idx}`)}
							<span class="grid h-12 w-9 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs sm:h-14 sm:w-11">
								{digit}
							</span>
						{/each}
					</div>
				</div>
			</div>

			<div class="rounded-xl border border-border bg-muted/20 p-3.5 sm:p-4">
				<PipePendingList {pending} capacity={PIPE_ROOM_CAPACITY} {now} {onSend} {onDeny} />
			</div>

			{#if waitingNotice}
				<p
					role="status"
					aria-live="polite"
					class="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-center text-xs font-medium text-destructive"
				>
					{waitingNotice}
				</p>
			{/if}

			<div class="grid gap-2">
				<div class="grid grid-cols-2 gap-2.5">
					<Button type="button" variant="outline" class="h-11 gap-2" onclick={copyCode}>
						{#if copied}
							<CheckIcon class="size-4 shrink-0" aria-hidden="true" />
							Copied!
						{:else}
							<CopyIcon class="size-4 shrink-0" aria-hidden="true" />
							Copy code
						{/if}
					</Button>
					<Button
						type="button"
						variant="outline"
						class="h-11 gap-2"
						onclick={() => {
							qrOpen = true;
						}}
					>
						<QrCodeIcon class="size-4 shrink-0" aria-hidden="true" />
						Show QR
					</Button>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					class="h-10 justify-self-center text-xs text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
					onclick={onCancel}
				>
					Cancel live transfer
				</Button>
			</div>

			<aside class="rounded-lg bg-muted/40 px-3.5 py-2.5 text-left text-xs text-muted-foreground">
				<p class="leading-relaxed">
					<strong class="font-medium text-foreground">Keep this tab open.</strong> Nothing is saved on the server — your browser sends the secret live when the recipient connects.
				</p>
			</aside>
		</section>
	{/if}
</div>

<QrModal bind:open={qrOpen} url={directReceiveUrl} />
