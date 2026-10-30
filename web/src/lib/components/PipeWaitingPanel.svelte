<script lang="ts">
import {
	CheckIcon,
	ClockIcon,
	CopyIcon,
	FlameIcon,
	QrCodeIcon,
	ShieldCheckIcon,
	XIcon,
	ZapIcon
} from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import QrModal from '#lib/components/QrModal.svelte';
import SuccessCheck from '#lib/components/SuccessCheck.svelte';
import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
import { formatPipeCode } from '#lib/crypto/pipe.js';
import { cn } from '#lib/utils.js';

type Props = {
	code: string;
	expiresInSeconds?: number;
	onCancel: () => void;
	onReset: () => void;
	isLiveSession?: boolean;
	statusText?: string;
	progressPercent?: number;
	completed?: boolean;
	totalBytes?: number;
	requireApproval?: boolean;
	matchCode?: string;
	approvalStatus?: 'idle' | 'pending' | 'approved' | 'rejected';
	onApprove?: () => void;
	onReject?: () => void;
	deviceInfo?: string;
};

let {
	code,
	expiresInSeconds = 180,
	onCancel,
	onReset,
	isLiveSession = false,
	statusText = 'Waiting for receiver...',
	progressPercent = 0,
	completed = false,
	totalBytes = 0,
	requireApproval = false,
	matchCode = '482',
	approvalStatus = 'idle',
	onApprove,
	onReject,
	deviceInfo = 'Chrome on Mobile'
}: Props = $props();

let remainingSeconds = $state(180);
let qrOpen = $state(false);
let copied = $state(false);

let timerInterval: number | null = null;

onMount(() => {
	remainingSeconds = expiresInSeconds;
	timerInterval = window.setInterval(() => {
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

// Direct receive URL that can be encoded in QR code
let directReceiveUrl = $derived(
	typeof window === 'undefined'
		? `https://flick.drop/#receive=${code}`
		: `${window.location.origin}/#receive=${code}`
);
</script>

<div class="grid gap-6 text-center">
	{#if completed}
		<div class="grid place-items-center gap-4">
			<SuccessCheck />
			<div class="grid gap-2">
				<p class="micro flex items-center justify-center gap-1.5 text-burn">
					<FlameIcon class="size-3.5" aria-hidden="true" />
					live pipe burned
				</p>
				<h2 class="font-serif text-3xl font-normal tracking-tight text-foreground">
					Transfer complete!
				</h2>
				<p class="text-sm text-muted-foreground">
					Streamed directly through memory and wiped. 0B persisted on server.
				</p>
			</div>

			<Button type="button" size="cta" class="w-full mt-2" onclick={onReset}>
				Send another secret
			</Button>
		</div>
	{:else}
		<!-- Active Waiting / Streaming Stage -->
		<div class="grid place-items-center gap-3">
			<div
				class="inline-flex items-center gap-1.5 rounded-full border border-primary/20 bg-primary/10 px-3 py-1 text-xs font-semibold text-primary"
			>
				<span class="size-2 rounded-full bg-primary animate-pulse" aria-hidden="true"></span>
				<span>{statusText}</span>
			</div>

			<h2 class="font-serif text-2xl font-normal text-foreground">
				Enter this code on receiving device
			</h2>
			<p class="text-xs text-muted-foreground max-w-sm">
				Open Flick on the receiver device and click <strong class="text-foreground">Enter code</strong> in the header:
			</p>

			<!-- 6-digit display boxes -->
			<div class="flex items-center justify-center gap-2 pt-2">
				<div class="flex gap-1.5 font-mono text-2xl font-bold sm:text-3xl">
					{#each firstHalf as digit}
						<span class="grid size-12 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs">
							{digit}
						</span>
					{/each}
				</div>
				<span class="font-mono text-xl text-muted-foreground px-1">-</span>
				<div class="flex gap-1.5 font-mono text-2xl font-bold sm:text-3xl">
					{#each secondHalf as digit}
						<span class="grid size-12 place-items-center rounded-xl border-2 border-primary/40 bg-muted/40 text-primary shadow-xs">
							{digit}
						</span>
					{/each}
				</div>
			</div>

			<!-- Expiration countdown -->
			<div class="flex items-center gap-2 font-mono text-xs text-muted-foreground pt-1">
				<ClockIcon class="size-3.5" aria-hidden="true" />
				<span>EXPIRES IN</span>
				<span class="tabular-nums font-bold text-foreground text-sm">
					{formatTime(remainingSeconds)}
				</span>
			</div>
		</div>

		<!-- 2-Step Confirmation Card -->
		{#if requireApproval && approvalStatus === 'pending'}
			<div class="rounded-2xl border-2 border-primary bg-primary/5 p-5 text-left space-y-4 shadow-md animate-in fade-in zoom-in-95">
				<div class="flex items-start justify-between">
					<div class="space-y-1">
						<span class="inline-flex items-center gap-1.5 rounded-full bg-primary/15 px-2.5 py-0.5 text-[11px] font-bold text-primary">
							<ShieldCheckIcon class="size-3.5" aria-hidden="true" />
							Receiver connected
						</span>
						<h3 class="text-base font-bold text-foreground">Confirm live transfer</h3>
						<p class="text-xs text-muted-foreground">
							Verify that this code matches the code shown on the receiver's device:
						</p>
					</div>
				</div>

				<!-- Match code callout -->
				<div class="flex flex-col items-center justify-center rounded-xl bg-background border border-primary/30 p-3 text-center shadow-xs">
					<span class="text-[10px] uppercase font-mono tracking-wider text-muted-foreground">Confirmation Code</span>
					<div class="font-mono text-3xl font-extrabold text-primary tracking-widest pt-0.5">
						{matchCode}
					</div>
				</div>

				<div class="flex items-center justify-between text-[11px] text-muted-foreground px-1">
					<span>Receiver: <strong class="text-foreground">{deviceInfo}</strong></span>
					<span>Status: <strong class="text-amber-500">Pending approval</strong></span>
				</div>

				<div class="grid grid-cols-2 gap-3 pt-1">
					<Button
						type="button"
						variant="outline"
						class="border-destructive/40 text-destructive hover:bg-destructive/10"
						onclick={onReject}
					>
						<XIcon class="size-4" aria-hidden="true" />
						<span>Deny transfer</span>
					</Button>
					<Button
						type="button"
						class="bg-primary text-primary-foreground font-bold hover:bg-primary/90 shadow-sm"
						onclick={onApprove}
					>
						<CheckIcon class="size-4" aria-hidden="true" />
						<span>Approve & send</span>
					</Button>
				</div>
			</div>
		{:else if requireApproval && approvalStatus === 'rejected'}
			<div class="rounded-xl border border-destructive/30 bg-destructive/10 p-4 text-center text-xs space-y-2">
				<p class="font-bold text-destructive">Transfer denied</p>
				<p class="text-muted-foreground text-[11px]">
					Transfer was denied. The session was burned and wiped from memory.
				</p>
				<Button type="button" size="sm" variant="outline" onclick={onReset}>
					Start new secret
				</Button>
			</div>
		{/if}

		<!-- Progress Bar when Streaming -->
		{#if progressPercent > 0 && progressPercent < 100}
			<div class="grid gap-1.5 text-left border rounded-xl p-3 bg-muted/30">
				<div class="flex justify-between text-xs font-mono text-muted-foreground">
					<span>Streaming encrypted chunks...</span>
					<span>{progressPercent}%</span>
				</div>
				<div class="h-2 w-full overflow-hidden rounded-full bg-muted">
					<div
						class="h-full bg-primary transition-all duration-200"
						style="width: {progressPercent}%"
					></div>
				</div>
			</div>
		{/if}

		<!-- Action buttons -->
		<div class="grid gap-2.5 pt-2">
			<Button
				type="button"
				variant="outline"
				class="h-11 w-full gap-2"
				onclick={copyCode}
			>
				<CopyIcon class="size-4" aria-hidden="true" />
				{copied ? 'Copied code!' : 'Copy 6-digit code'}
			</Button>

			<Button
				type="button"
				variant="outline"
				class="h-11 w-full gap-2"
				onclick={() => {
					qrOpen = true;
				}}
			>
				<QrCodeIcon class="size-4" aria-hidden="true" />
				Show QR (Optional)
			</Button>

			<Button
				type="button"
				variant="ghost"
				class="h-9 w-full text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
				onclick={onCancel}
			>
				Cancel code drop
			</Button>
		</div>

		<aside class="flex gap-3 rounded-lg bg-muted/40 p-4 text-left">
			<ZapIcon class="mt-0.5 size-4 shrink-0 text-primary" aria-hidden="true" />
			<div class="grid gap-1 text-xs">
				<strong class="font-medium text-foreground">Direct in-memory transfer</strong>
				<p class="text-muted-foreground">
					No files or messages are saved to disk or Object Storage. The session automatically burns on completion or timeout.
				</p>
			</div>
		</aside>
	{/if}
</div>

<QrModal bind:open={qrOpen} url={directReceiveUrl} />
