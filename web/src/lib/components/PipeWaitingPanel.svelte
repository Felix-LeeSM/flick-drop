<script lang="ts">
import { CheckIcon, CopyIcon, QrCodeIcon } from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import LockStageCard from '#lib/components/LockStageCard.svelte';
import PipePendingList from '#lib/components/PipePendingList.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { formatPipeCode } from '#lib/crypto/pipe.js';
import { t } from '#lib/i18n/index.js';
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
	statusText: statusTextProp,
	progressPercent = 0,
	completed = false,
	failed = false,
	pending = [],
	onSend = () => undefined,
	onDeny = () => undefined
}: Props = $props();

let remainingSeconds = $state(180);
// The ring plays its lock or failure beat before the result shows; the relay reports 25/50/100.
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

const statusText = $derived(statusTextProp ?? t().live.waiting.defaultStatus);
const normalStatusMessages = $derived(
	new Set([
		'',
		t().live.waiting.defaultStatus,
		t().live.relay.waitingForCode,
		t().live.relay.initializing,
		t().live.relay.sendingChosen,
		t().live.relay.encrypting,
		t().live.relay.streaming,
		t().live.relay.complete
	])
);
const waitingNotice = $derived(normalStatusMessages.has(statusText) ? '' : statusText);

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
			label={t().live.waiting.stageLabel}
			caption={t().live.waiting.stageCaption}
			status={stagePhase === 'finishing'
				? t().live.waiting.stageCleared
				: stagePhase === 'failing'
					? statusText
					: progressPercent >= 50
						? t().live.waiting.stageStreaming
						: t().live.waiting.stageEncrypting}
			progress={progressPercent / 100}
			cancellable={stagePhase === 'waiting'}
			{onCancel}
		/>
	{:else if completed}
		<section class="grid gap-6">
			<div class="grid gap-1.5">
				<p class="micro flex items-center gap-1.5 text-success">
					<CheckIcon class="size-3.5 shrink-0" aria-hidden="true" />
					{t().live.waiting.completeLabel}
				</p>
				<h1 class="page-title">{t().live.waiting.completeTitle}</h1>
				<p class="text-sm text-muted-foreground">
					{t().live.waiting.completeBody}
				</p>
			</div>

			<Button type="button" size="cta" class="w-full" onclick={onReset}>
				{t().live.waiting.sendAnother}
			</Button>
		</section>
	{:else if failed && (stageSettled || !streaming)}
		<!-- An ended session leaves a dead code, so the code view is not offered. -->
		<section class="grid gap-6" role="alert">
			<div class="grid gap-1.5">
				<p class="micro text-destructive">{t().live.waiting.endedLabel}</p>
				<h1 class="page-title">{t().live.waiting.failedTitle}</h1>
				<p class="text-sm text-muted-foreground">{statusText}</p>
				<p class="text-xs text-muted-foreground">{t().live.waiting.failedBody}</p>
			</div>
			<Button type="button" size="cta" variant="outline" class="w-full" onclick={onReset}>
				{t().live.waiting.startNew}
			</Button>
		</section>
	{:else}
		<section class="grid gap-5 rounded-xl border border-border bg-card p-5 shadow-sm" aria-labelledby="pipe-waiting-heading">
			<h1 id="pipe-waiting-heading" class="sr-only">{t().live.waiting.pageTitle}</h1>
			<div class="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3.5">
				<div class="flex items-center gap-2">
					<span class="size-2 rounded-full bg-primary animate-pulse motion-reduce:animate-none" aria-hidden="true"></span>
					<span class="micro text-foreground">{t().live.waiting.codeLabel}</span>
				</div>
				<span class="font-mono text-xs tabular-nums text-muted-foreground">
					{t().live.waiting.expiresBefore}<strong class="font-bold text-foreground">{formatTime(remainingSeconds)}</strong>{t().live.waiting.expiresAfter}
				</span>
			</div>

			<div class="grid gap-3 py-1 text-center">
				<p class="text-sm text-muted-foreground">
					{t().live.waiting.recipientHintBefore}<strong class="font-medium text-foreground">{t().common.enterCode}</strong>{t().live.waiting.recipientHintAfter}
				</p>
				<div class="flex min-w-0 items-center justify-center gap-2" aria-label={t().live.waiting.codeAria(formattedCode)}>
					<div class="flex min-w-0 gap-1.5 font-mono text-2xl font-bold sm:text-[1.75rem]">
						{#each firstHalf as digit, idx (`a-${idx}`)}
							<span class="grid h-12 w-9 min-w-0 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs sm:h-14 sm:w-11">
								{digit}
							</span>
						{/each}
					</div>
					<span class="font-mono text-xl text-muted-foreground" aria-hidden="true">-</span>
					<div class="flex min-w-0 gap-1.5 font-mono text-2xl font-bold sm:text-[1.75rem]">
						{#each secondHalf as digit, idx (`b-${idx}`)}
							<span class="grid h-12 w-9 min-w-0 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs sm:h-14 sm:w-11">
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
					<Button type="button" variant="outline" class="h-auto min-h-11 gap-2 whitespace-normal py-2" onclick={copyCode}>
						{#if copied}
							<CheckIcon class="size-4 shrink-0" aria-hidden="true" />
							{t().live.waiting.copied}
						{:else}
							<CopyIcon class="size-4 shrink-0" aria-hidden="true" />
							{t().live.waiting.copyCode}
						{/if}
					</Button>
					<Button
						type="button"
						variant="outline"
						class="h-auto min-h-11 gap-2 whitespace-normal py-2"
						onclick={() => {
							qrOpen = true;
						}}
					>
						<QrCodeIcon class="size-4 shrink-0" aria-hidden="true" />
						{t().live.waiting.showQr}
					</Button>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					class="h-10 justify-self-center text-xs text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
					onclick={onCancel}
				>
					{t().live.waiting.cancel}
				</Button>
			</div>

			<aside class="rounded-lg bg-muted/40 px-3.5 py-2.5 text-left text-xs text-muted-foreground">
				<p class="leading-relaxed">
					<strong class="font-medium text-foreground">{t().live.waiting.keepOpen}</strong> {t().live.waiting.keepOpenBody}
				</p>
			</aside>
		</section>
	{/if}
</div>

<QrModal bind:open={qrOpen} url={directReceiveUrl} />
