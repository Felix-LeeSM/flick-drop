<script lang="ts">
import {
	CheckIcon,
	CopyIcon,
	DownloadIcon,
	FlameIcon,
	LoaderCircleIcon,
	LogOutIcon,
	UsersIcon,
	XIcon,
	ZapIcon
} from '@lucide/svelte';
import { Dialog } from 'bits-ui';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import type { ReceivedPipePayload } from '#lib/components/PipeReceivedSecretView.svelte';
import ProgressRingLock from '#lib/components/ProgressRingLock.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import VerifyDigits from '#lib/components/VerifyDigits.svelte';
import { decryptWith } from '#lib/crypto/age.js';
import {
	createPipeReceiverKey,
	decryptPipeFilename,
	formatPipeCode,
	joinPipe,
	normalizePipeCode,
	PipeRelayError,
	receivePipeStream,
	streamToBytes,
	streamToText,
	waitForPipeSelection
} from '#lib/crypto/pipe.js';
import { t } from '#lib/i18n/index.js';
import {
	holdMinimumStage,
	STAGE_FAIL_MS,
	STAGE_FINISH_MS,
	type StagePhase,
	sleep
} from '#lib/stage-timing.js';
import {
	rememberDeclinedCode,
	sessionExpiresAt,
	wasDeclinedCode
} from '#lib/state/pipe-declined.js';
import { cn, formatBytes } from '#lib/utils.js';

type Props = {
	open?: boolean;
	apiBaseUrl?: string;
	initialCode?: string;
	onCodeReceived?: (payload: ReceivedPipePayload) => void;
};

let {
	open = $bindable(false),
	apiBaseUrl = DEFAULT_API_BASE_URL,
	initialCode = '',
	onCodeReceived
}: Props = $props();

// verify: this device shows its own number while the sender picks.
// declined / not-chosen / room-full end this code for this device.
type ReceiveState =
	| 'input'
	| 'connecting'
	| 'verify'
	| 'streaming'
	| 'done-file'
	| 'done-text'
	| 'declined'
	| 'not-chosen'
	| 'room-full'
	| 'error';

let rawCode = $state('');
let codeFocused = $state(false);

let currentState = $state<ReceiveState>('input');
// Entering the code is step 1; waiting on the sender's pick and the transfer is step 2.
const step = $derived(
	currentState === 'input'
		? 1
		: currentState === 'connecting' || currentState === 'verify' || currentState === 'streaming'
			? 2
			: undefined
);
const ringVisible = $derived(currentState === 'connecting' || currentState === 'streaming');
// Unlock ring while connected: it keeps its minimum and plays the unlock (or
// failure) beat before the received content or the error view shows.
let ringPhase = $state<StagePhase>('waiting');
// True once the content is decrypted: the relay has burned it, so the remaining
// ring beat is pacing only and Cancel would throw the secret away.
let decrypted = $state(false);
let errorMessage = $state('');
// The ring's status while connected; read from the catalog on render.
let statusKey = $state<'connectingStatus' | 'decryptingStatus' | null>(null);
// This device's own server-drawn number, shown while the sender picks.
let verifyCode = $state('');

// Result payloads
let receivedText = $state('');
let receivedFileName = $state('');
let receivedFileSize = $state(0);
let receivedFileBlob = $state<Blob | null>(null);
let copiedText = $state(false);

let abortController = $state<AbortController | null>(null);

function clearPayloadState(): void {
	receivedText = '';
	receivedFileName = '';
	receivedFileSize = 0;
	receivedFileBlob = null;
	copiedText = false;
}

function reset(): void {
	abortController?.abort();
	abortController = null;
	rawCode = '';
	currentState = 'input';
	ringPhase = 'waiting';
	decrypted = false;
	errorMessage = '';
	statusKey = null;
	clearPayloadState();
	verifyCode = '';
}

let previousOpen = false;
$effect(() => {
	if (!open) {
		previousOpen = false;
		abortController?.abort();
		abortController = null;
		clearPayloadState();
		return;
	}
	if (!previousOpen) {
		previousOpen = true;
		reset();
	}
	if (initialCode && rawCode === '') {
		const digits = initialCode.replace(/\D/g, '').slice(0, 6);
		rawCode = digits;
		if (digits.length === 6) {
			void startReceive();
		}
	}
});

function handleCodeInput(event: Event): void {
	const target = event.target as HTMLInputElement;
	// Digits only, so a pasted "741-928" still fills all six caps. The field is invisible, so write
	// the cleaned value back: a stray letter left in it would otherwise swallow the next Backspace.
	const sanitized = target.value.replace(/\D/g, '').slice(0, 6);
	target.value = sanitized;
	rawCode = sanitized;

	if (sanitized.length === 6) {
		void startReceive();
	}
}

// The rejections that end this code for this device with their own screen.
function terminalState(err: unknown): ReceiveState | undefined {
	if (!(err instanceof PipeRelayError)) {
		return undefined;
	}
	switch (err.reason) {
		case 'declined':
			return 'declined';
		case 'not_selected':
			return 'not-chosen';
		case 'room_full':
			return 'room-full';
		default:
			return undefined;
	}
}

// One receive attempt's timing: when its ring started and the local clock time
// the session window closes, fixed when the join response arrived.
type ReceiveAttempt = {
	normalized: string;
	signal: AbortSignal;
	stageStartedAt: number;
	sessionExpiresAt: number;
};

async function startReceive(): Promise<void> {
	let normalized: string;
	try {
		normalized = normalizePipeCode(rawCode);
	} catch {
		errorMessage = t().live.receive.invalidCode;
		currentState = 'error';
		return;
	}

	// A device the sender declined does not even ask again within that session.
	if (await wasDeclinedCode(normalized)) {
		currentState = 'declined';
		return;
	}

	currentState = 'connecting';
	ringPhase = 'waiting';
	decrypted = false;
	verifyCode = '';
	statusKey = 'connectingStatus';
	errorMessage = '';
	const controller = new AbortController();
	abortController = controller;
	const attempt: ReceiveAttempt = {
		normalized,
		signal: controller.signal,
		stageStartedAt: performance.now(),
		sessionExpiresAt: Date.now()
	};

	try {
		await joinAndReceive(attempt);
	} catch (err) {
		await endReceive(attempt, err);
	}
}

async function joinAndReceive(attempt: ReceiveAttempt): Promise<void> {
	const { normalized, signal } = attempt;
	const targetBaseUrl = apiBaseUrl || DEFAULT_API_BASE_URL;
	// A fresh key per attempt: only its recipient goes to the API, and the
	// identity lives in this function's memory until the attempt ends.
	const { identity, recipient } = await createPipeReceiverKey();
	const join = await joinPipe(targetBaseUrl, normalized, recipient, fetch, signal);
	attempt.sessionExpiresAt = sessionExpiresAt(join.expiresIn);
	verifyCode = join.verifyCode;
	currentState = 'verify';
	// Short status polls keep this device in the sender's list; /receive
	// opens only once picked, so no request sits silent behind a proxy.
	await waitForPipeSelection(targetBaseUrl, normalized, join.connectionId, fetch, signal);

	const result = await receivePipeStream(
		targetBaseUrl,
		normalized,
		join.connectionId,
		fetch,
		signal
	);
	// The ring starts when the chosen device's stream arrives.
	attempt.stageStartedAt = performance.now();
	currentState = 'streaming';
	statusKey = 'decryptingStatus';

	const decryptedStream = await decryptWith(identity, result.stream);

	const outcome = await storeReceived(result, decryptedStream, identity, normalized, signal);
	if (!outcome || !(await settleRing(attempt.stageStartedAt, 'finishing', signal))) {
		return;
	}
	if (onCodeReceived) {
		const payload = outcome.payload;
		clearPayloadState();
		open = false;
		onCodeReceived(payload);
		return;
	}
	currentState = outcome.doneState;
}

async function endReceive(attempt: ReceiveAttempt, err: unknown): Promise<void> {
	if (attempt.signal.aborted) {
		return;
	}
	const endState = terminalState(err) ?? 'error';
	if (endState === 'declined') {
		await rememberDeclinedCode(attempt.normalized, attempt.sessionExpiresAt);
	}
	errorMessage = err instanceof Error ? err.message : t().live.receive.failedFallback;
	// Only a visible ring plays the failure beat; the number view ends at once.
	if (!ringVisible || (await settleRing(attempt.stageStartedAt, 'failing', attempt.signal))) {
		currentState = endState;
	}
}

// Keeps the decrypted content and marks it received; null when the receive was
// cancelled meanwhile. When handing off to the page view, the file Blob transfers
// to PipeReceivedSecretView without triggering an unsolicited modal download.
async function storeReceived(
	result: Awaited<ReturnType<typeof receivePipeStream>>,
	decryptedStream: ReadableStream<Uint8Array>,
	identity: string,
	code: string,
	signal: AbortSignal
): Promise<{ doneState: 'done-file' | 'done-text'; payload: ReceivedPipePayload } | null> {
	if (result.kind !== 'file') {
		receivedText = await streamToText(decryptedStream);
		decrypted = true;
		return {
			doneState: 'done-text',
			payload: { kind: 'text', code, text: receivedText }
		};
	}
	const bytes = await streamToBytes(decryptedStream);
	receivedFileSize = bytes.length;
	receivedFileName = await decryptFilename(result.encryptedFilename, identity);
	if (signal.aborted) {
		return null;
	}
	const blob = new Blob([bytes as Uint8Array<ArrayBuffer>]);
	receivedFileBlob = blob;
	decrypted = true;
	if (!onCodeReceived) {
		downloadFile();
	}
	return {
		doneState: 'done-file',
		payload: {
			kind: 'file',
			code,
			fileBlob: blob,
			fileName: receivedFileName,
			fileSize: bytes.length
		}
	};
}

// The sender encrypts the filename separately; a missing or unreadable one falls
// back to a generic name rather than failing the received file.
async function decryptFilename(
	encryptedFilename: string | undefined,
	identity: string
): Promise<string> {
	if (!encryptedFilename) {
		return 'downloaded-file';
	}
	try {
		return await decryptPipeFilename(identity, encryptedFilename);
	} catch {
		return 'downloaded-file';
	}
}

// Holds the ring for its minimum, then plays the ending beat. False when the
// receive was cancelled or reset meanwhile.
async function settleRing(
	startedAt: number,
	ending: 'finishing' | 'failing',
	signal: AbortSignal
): Promise<boolean> {
	await holdMinimumStage(startedAt);
	if (signal.aborted) {
		return false;
	}
	ringPhase = ending;
	await sleep(ending === 'finishing' ? STAGE_FINISH_MS : STAGE_FAIL_MS);
	return !signal.aborted;
}

function downloadFile(): void {
	if (!receivedFileBlob) {
		return;
	}
	const url = URL.createObjectURL(receivedFileBlob);
	const a = document.createElement('a');
	a.href = url;
	a.download = receivedFileName;
	document.body.appendChild(a);
	a.click();
	document.body.removeChild(a);
	URL.revokeObjectURL(url);
}

async function copyText(): Promise<void> {
	if (!receivedText) {
		return;
	}
	try {
		await navigator.clipboard.writeText(receivedText);
		copiedText = true;
		setTimeout(() => {
			copiedText = false;
		}, 2000);
	} catch {
		// Fallback ignored
	}
}
</script>

<Dialog.Root bind:open={open}>
	<Dialog.Portal>
		<Dialog.Overlay
			class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0"
		/>
		<Dialog.Content
			class="fixed left-1/2 top-3 z-50 grid max-h-[calc(100dvh-1.5rem)] w-[23rem] max-w-[calc(100vw-1.5rem)] grid-cols-[minmax(0,1fr)] -translate-x-1/2 gap-5 overflow-y-auto sm:top-1/2 sm:-translate-y-1/2 rounded-2xl border border-border bg-card p-6 text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			{#if currentState === 'declined'}
				<div class="relative z-10 -mb-8 flex items-center justify-end">
					<Dialog.Close
						class="-mr-1.5 -mt-1.5 ml-auto grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
						aria-label={t().live.receive.close}
					>
						<XIcon class="size-4" aria-hidden="true" />
					</Dialog.Close>
				</div>
				<Dialog.Title class="sr-only">{t().live.receive.declinedTitle}</Dialog.Title>
				<Dialog.Description class="sr-only">
					{t().live.receive.declinedDescription}
				</Dialog.Description>
			{:else}
				<!-- The dialog keeps one name in every other step; the visible heading names the step. -->
				<Dialog.Title class="sr-only">{t().live.receive.dialogTitle}</Dialog.Title>
				<Dialog.Description class="sr-only">
					{t().live.receive.dialogDescription}
				</Dialog.Description>

				<div class="flex items-start justify-between gap-3">
					{#if step}
						<div class="grid gap-1">
							<div class="flex flex-wrap items-center gap-1.5">
								<span class="whitespace-nowrap rounded-md bg-primary/12 px-2 py-0.5 font-mono text-[0.625rem] font-bold text-primary">
									{t().live.receive.step(step)}
								</span>
								<span class="micro whitespace-nowrap text-muted-foreground">{t().live.receive.zeroDisk}</span>
							</div>
							<h3 class="font-serif text-2xl leading-tight">
								{step === 1
									? t().live.receive.headingConnect
									: currentState === 'verify'
										? t().live.receive.headingVerify
										: currentState === 'streaming'
											? t().live.receive.headingReceiving
											: t().live.receive.headingConnecting}
							</h3>
						</div>
					{/if}
					<Dialog.Close
						class="-mr-1.5 -mt-1.5 ml-auto grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
						aria-label={t().live.receive.close}
					>
						<XIcon class="size-4" aria-hidden="true" />
					</Dialog.Close>
				</div>
			{/if}

			{#if currentState === 'input'}
				<div class="grid gap-5 text-center">
					<div class="grid gap-2">
						<Label for="pipe-code-input" class="sr-only">{t().live.receive.codeInputLabel}</Label>
						<!-- Six key caps mirror one transparent input laid over them, so typing, pasting,
						     and the phone's number pad all go through a single real field. -->
						<div class="relative flex items-center justify-center gap-1.5">
							{#each [0, 1, 2, 3, 4, 5] as slot (slot)}
								{@const digit = rawCode[slot] ?? ''}
								<span
									class={cn(
										'grid h-12 min-w-0 max-w-11 flex-1 place-items-center rounded-lg border-x border-t border-b-3 font-mono text-xl font-bold transition-transform motion-reduce:transition-none',
										digit
											? '-translate-y-0.5 border-primary bg-primary/10 text-primary'
											: 'border-border bg-muted/30 text-muted-foreground',
										// The transparent outline shows only in forced colors, where the ring is dropped.
										codeFocused &&
											slot === Math.min(rawCode.length, 5) &&
											'outline-2 outline-transparent ring-3 ring-ring/50'
									)}
									aria-hidden="true"
								>
									{digit || '0'}
								</span>
								{#if slot === 2}
									<span class="font-mono text-muted-foreground" aria-hidden="true">-</span>
								{/if}
							{/each}
							<input
								id="pipe-code-input"
								type="text"
								inputmode="numeric"
								pattern="[0-9]*"
								autocomplete="off"
								value={rawCode}
								oninput={handleCodeInput}
								onfocus={() => {
									codeFocused = true;
								}}
								onblur={() => {
									codeFocused = false;
								}}
								class="absolute inset-0 size-full cursor-text text-base opacity-0"
							/>
						</div>
						<p class="text-[0.6875rem] text-muted-foreground">
							{t().live.receive.autoConnect}
						</p>
					</div>

					<div class="grid gap-2">
						<Button type="button" size="cta" disabled={rawCode.length !== 6} onclick={() => startReceive()}>
							<ZapIcon class="size-4" aria-hidden="true" />
							{t().live.receive.connect}
						</Button>
						<p class="text-[0.6875rem] text-muted-foreground">
							{t().live.receive.nextHint}
						</p>
					</div>
				</div>
			{:else if currentState === 'connecting' || currentState === 'streaming'}
				<div class="grid place-items-center gap-4 text-center">
					<!-- The same unlock ring as opening a link; the receiver has no byte progress, so it creeps. -->
					<ProgressRingLock mode="unlock" phase={ringPhase} />
					<div class="grid gap-1">
						<p
							role="status"
							aria-live="polite"
							class={cn('text-sm', ringPhase === 'failing' ? 'text-destructive' : 'text-muted-foreground')}
						>
							{ringPhase === 'finishing'
								? t().live.receive.unlocked
								: ringPhase === 'failing'
									? errorMessage
									: statusKey
										? t().live.receive[statusKey]
										: ''}
						</p>
						<p class="font-mono text-xs text-muted-foreground">{t().live.receive.pipe(formatPipeCode(rawCode))}</p>
					</div>

					{#if !decrypted}
						<Button
							type="button"
							variant="ghost"
							size="sm"
							class="text-xs text-muted-foreground hover:text-foreground"
							onclick={() => reset()}
						>
							{t().live.receive.cancel}
						</Button>
					{/if}
				</div>
			{:else if currentState === 'verify'}
				<div class="grid grid-cols-[minmax(0,1fr)] gap-5 text-center">
					<p class="text-xs text-muted-foreground">
						{t().live.receive.verifyHint}
					</p>
					<VerifyDigits code={verifyCode} label={t().live.receive.yourNumber(verifyCode.split('').join(' '))} />
					<p class="flex items-center justify-center gap-2 text-sm text-muted-foreground" role="status">
						<LoaderCircleIcon class="size-4 animate-spin text-primary motion-reduce:animate-none" aria-hidden="true" />
						{t().live.receive.waitingForPick}
					</p>
					<p class="font-mono text-xs text-muted-foreground">{t().live.receive.pipe(formatPipeCode(rawCode))}</p>
					<Button
						type="button"
						variant="ghost"
						size="sm"
						class="h-auto min-h-11 justify-self-center whitespace-normal py-2 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
						onclick={() => {
							open = false;
						}}
					>
						{t().live.receive.notMine}
					</Button>
				</div>
			{:else if currentState === 'declined'}
				<div class="grid justify-items-center gap-5 py-1 text-center" role="status">
					<div
						class="grid size-12 place-items-center rounded-full border border-destructive/30 bg-destructive/10 text-destructive"
						aria-hidden="true"
					>
						<XIcon class="size-5" />
					</div>
					<div class="grid justify-items-center gap-2">
						<p class="micro text-muted-foreground">
							{t().live.receive.declinedLabel(formatPipeCode(rawCode))}
						</p>
						<h3 class="font-serif text-[1.625rem] leading-[1.15] font-normal tracking-[-0.01em] text-balance text-foreground">
							{t().live.receive.declinedHeading}
						</h3>
						<p class="max-w-xs text-[0.8125rem] leading-relaxed text-muted-foreground">
							{t().live.receive.declinedBody}
						</p>
					</div>
					<Button
						type="button"
						variant="outline"
						class="h-11 w-full rounded-[10px] font-medium"
						onclick={() => {
							open = false;
						}}
					>
						{t().live.receive.close}
					</Button>
				</div>
			{:else if currentState === 'not-chosen'}
				<!-- Ends plainly without calling it an attack: the sender picked another device. -->
				<div class="grid place-items-center gap-3 py-2 text-center" role="status">
					<div class="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
						<LogOutIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">{t().live.receive.notChosenHeading}</h4>
						<p class="text-xs text-muted-foreground">
							{t().live.receive.notChosenBody}
						</p>
					</div>
					<Button type="button" variant="outline" class="mt-1 w-full" onclick={() => reset()}>
						{t().live.receive.enterNewCode}
					</Button>
				</div>
			{:else if currentState === 'room-full'}
				<div class="grid place-items-center gap-3 py-2 text-center" role="alert">
					<div class="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
						<UsersIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">{t().live.receive.roomFullHeading}</h4>
						<p class="text-xs text-muted-foreground">
							{t().live.receive.roomFullBody}
						</p>
					</div>
					<Button type="button" variant="outline" class="mt-1 w-full" onclick={() => reset()}>
						{t().live.receive.enterNewCode}
					</Button>
				</div>
			{:else if currentState === 'done-file'}
				<div class="grid place-items-center gap-4 text-center">
					<div class="grid size-12 place-items-center rounded-full bg-emerald-500/15 text-emerald-500">
						<CheckIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-xl">{t().live.receive.fileDownloaded}</h4>
						<p class="text-xs text-muted-foreground">{t().live.receive.fileSaved}</p>
					</div>
					<div class="flex w-full items-center justify-between rounded-xl border border-border bg-muted/40 p-3 text-left">
						<div class="min-w-0 pr-2">
							<p class="truncate text-xs font-semibold text-foreground">{receivedFileName}</p>
							<p class="text-[11px] text-muted-foreground">{formatBytes(receivedFileSize)}</p>
						</div>
						<Button type="button" size="sm" variant="outline" class="gap-1.5" onclick={downloadFile}>
							<DownloadIcon class="size-3.5" aria-hidden="true" />
							{t().live.receive.downloadAgain}
						</Button>
					</div>
					<Button type="button" class="w-full mt-2" onclick={() => { open = false; reset(); }}>
						{t().live.receive.done}
					</Button>
				</div>
			{:else if currentState === 'done-text'}
				<div class="grid gap-4 text-left">
					<div class="flex items-center justify-between">
						<div class="flex items-center gap-1.5 text-xs font-semibold text-emerald-500">
							<CheckIcon class="size-4" aria-hidden="true" />
							<span>{t().live.receive.decryptedInMemory}</span>
						</div>
						<span class="micro flex items-center gap-1 text-burn">
							<FlameIcon class="size-3" aria-hidden="true" />
							{t().live.receive.burned}
						</span>
					</div>

					<div class="relative">
						<textarea
							readonly
							aria-label={t().live.receive.decryptedContent}
							class="min-h-36 w-full rounded-xl border border-border bg-muted/30 p-3 pr-28 font-mono text-xs text-foreground focus:outline-none"
							value={receivedText}
						></textarea>
						<Button
							type="button"
							size="sm"
							variant="secondary"
							class="absolute right-2.5 top-2.5 gap-1.5 h-8 text-xs font-medium"
							onclick={copyText}
						>
							<CopyIcon class="size-3.5" aria-hidden="true" />
							{copiedText ? t().live.receive.copied : t().live.receive.copy}
						</Button>
					</div>

					<Button type="button" class="w-full mt-2" onclick={() => { open = false; reset(); }}>
						{t().live.receive.done}
					</Button>
				</div>
			{:else if currentState === 'error'}
				<div class="grid place-items-center gap-3 py-4 text-center">
					<div class="grid size-12 place-items-center rounded-full bg-destructive/15 text-destructive">
						<XIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">{t().live.receive.failedHeading}</h4>
						<p class="text-xs text-destructive">{errorMessage}</p>
					</div>
					<Button type="button" variant="outline" class="w-full mt-2" onclick={() => reset()}>
						{t().live.receive.tryAgain}
					</Button>
				</div>
			{/if}
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
