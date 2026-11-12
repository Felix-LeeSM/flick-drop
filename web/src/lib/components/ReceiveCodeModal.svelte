<script lang="ts">
import {
	CheckIcon,
	CopyIcon,
	DownloadIcon,
	FlameIcon,
	LoaderCircleIcon,
	LockKeyholeIcon,
	LogOutIcon,
	ShieldCheckIcon,
	UsersIcon,
	XIcon,
	ZapIcon
} from '@lucide/svelte';
import { Dialog } from 'bits-ui';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
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
};

let {
	open = $bindable(false),
	apiBaseUrl = DEFAULT_API_BASE_URL,
	initialCode = ''
}: Props = $props();

// verify: confirmation is on and this device shows its own number while the
// sender picks. declined / not-chosen / room-full end this code for this device.
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
let statusMessage = $state('');
// This device's own server-drawn number, shown while the sender picks.
let verifyCode = $state('');

// Result payloads
let receivedText = $state('');
let receivedFileName = $state('');
let receivedFileSize = $state(0);
let receivedFileBlob = $state<Blob | null>(null);
let copiedText = $state(false);

let abortController = $state<AbortController | null>(null);

function reset(): void {
	abortController?.abort();
	abortController = null;
	rawCode = '';
	currentState = 'input';
	ringPhase = 'waiting';
	decrypted = false;
	errorMessage = '';
	statusMessage = '';
	receivedText = '';
	receivedFileName = '';
	receivedFileSize = 0;
	receivedFileBlob = null;
	copiedText = false;
	verifyCode = '';
}

$effect(() => {
	if (!open) {
		reset();
	} else if (initialCode && rawCode === '') {
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
		errorMessage = 'Please enter a valid 6-digit number';
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
	statusMessage = 'Connecting to the sender…';
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
	if (join.requireConfirmation && join.verifyCode) {
		verifyCode = join.verifyCode;
		currentState = 'verify';
		// Short status polls keep this device in the sender's list; /receive
		// opens only once picked, so no request sits silent behind a proxy.
		await waitForPipeSelection(targetBaseUrl, normalized, join.connectionId, fetch, signal);
	} else {
		statusMessage = 'Waiting for the sender…';
	}

	const result = await receivePipeStream(
		targetBaseUrl,
		normalized,
		join.connectionId,
		fetch,
		signal
	);
	if (currentState === 'verify') {
		// The ring starts when the chosen device's stream arrives.
		attempt.stageStartedAt = performance.now();
	}
	currentState = 'streaming';
	statusMessage = 'Decrypting in your browser…';

	const decryptedStream = await decryptWith(identity, result.stream);

	const doneState = await storeReceived(result, decryptedStream, identity, signal);
	if (doneState && (await settleRing(attempt.stageStartedAt, 'finishing', signal))) {
		currentState = doneState;
	}
}

async function endReceive(attempt: ReceiveAttempt, err: unknown): Promise<void> {
	if (attempt.signal.aborted) {
		return;
	}
	const endState = terminalState(err) ?? 'error';
	if (endState === 'declined') {
		await rememberDeclinedCode(attempt.normalized, attempt.sessionExpiresAt);
	}
	errorMessage = err instanceof Error ? err.message : 'Transfer failed';
	// Only a visible ring plays the failure beat; the number view ends at once.
	if (!ringVisible || (await settleRing(attempt.stageStartedAt, 'failing', attempt.signal))) {
		currentState = endState;
	}
}

// Keeps the decrypted content and marks it received; null when the receive was
// cancelled meanwhile. A file is saved right away so closing the dialog during
// the ring beat cannot lose it.
async function storeReceived(
	result: Awaited<ReturnType<typeof receivePipeStream>>,
	decryptedStream: ReadableStream<Uint8Array>,
	identity: string,
	signal: AbortSignal
): Promise<'done-file' | 'done-text' | null> {
	if (result.kind !== 'file') {
		receivedText = await streamToText(decryptedStream);
		decrypted = true;
		return 'done-text';
	}
	const bytes = await streamToBytes(decryptedStream);
	receivedFileSize = bytes.length;
	receivedFileName = await decryptFilename(result.encryptedFilename, identity);
	if (signal.aborted) {
		return null;
	}
	receivedFileBlob = new Blob([bytes as Uint8Array<ArrayBuffer>]);
	decrypted = true;
	downloadFile();
	return 'done-file';
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
			class="fixed left-1/2 top-1/2 z-50 grid w-[23rem] max-w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 gap-5 rounded-2xl border border-border bg-card p-6 text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<!-- The dialog keeps one name in every step; the visible heading names the step. -->
			<Dialog.Title class="sr-only">Receive secret with code</Dialog.Title>
			<Dialog.Description class="sr-only">
				Enter a 6-digit code to connect to the sender's live memory pipe and receive encrypted data.
			</Dialog.Description>

			<div class="flex items-start justify-between gap-3">
				{#if step}
					<div class="grid gap-1">
						<div class="flex items-center gap-1.5">
							<span class="rounded-md bg-primary/12 px-2 py-0.5 font-mono text-[0.625rem] font-bold text-primary">
								STEP {step} OF 2
							</span>
							<span class="micro text-muted-foreground">0B disk relay</span>
						</div>
						<h3 class="font-serif text-2xl leading-tight">
							{step === 1
								? 'Connect to sender'
								: currentState === 'verify'
									? 'Read this to the sender'
									: currentState === 'streaming'
										? 'Receiving'
										: 'Connecting'}
						</h3>
					</div>
				{/if}
				<Dialog.Close
					class="-mr-1.5 -mt-1.5 ml-auto grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
					aria-label="Close"
				>
					<XIcon class="size-4" aria-hidden="true" />
				</Dialog.Close>
			</div>

			{#if currentState === 'input'}
				<div class="grid gap-5 text-center">
					<div class="grid gap-2">
						<Label for="pipe-code-input" class="sr-only">6-digit transfer code</Label>
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
							Auto-connects immediately when 6 digits are typed.
						</p>
					</div>

					<div class="grid gap-2">
						<Button type="button" size="cta" disabled={rawCode.length !== 6} onclick={() => startReceive()}>
							<ZapIcon class="size-4" aria-hidden="true" />
							Connect
						</Button>
						<p class="text-[0.6875rem] text-muted-foreground">
							If the sender asked for confirmation, you'll read them a 3-digit number.
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
								? 'Unlocked'
								: ringPhase === 'failing'
									? errorMessage
									: statusMessage}
						</p>
						<p class="font-mono text-xs text-muted-foreground">Pipe: {formatPipeCode(rawCode)}</p>
					</div>

					{#if !decrypted}
						<Button
							type="button"
							variant="ghost"
							size="sm"
							class="text-xs text-muted-foreground hover:text-foreground"
							onclick={() => reset()}
						>
							Cancel
						</Button>
					{/if}
				</div>
			{:else if currentState === 'verify'}
				<div class="grid gap-5 text-center">
					<p class="text-xs text-muted-foreground">
						Read this number to the sender out loud or in chat. They pick your device by this number.
					</p>
					<VerifyDigits code={verifyCode} label="Your number {verifyCode.split('').join(' ')}" />
					<p class="flex items-center justify-center gap-2 text-sm text-muted-foreground" role="status">
						<LoaderCircleIcon class="size-4 animate-spin text-primary motion-reduce:animate-none" aria-hidden="true" />
						Waiting for the sender to pick…
					</p>
					<p class="font-mono text-xs text-muted-foreground">Pipe: {formatPipeCode(rawCode)}</p>
					<Button
						type="button"
						variant="ghost"
						size="sm"
						class="h-11 justify-self-center text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
						onclick={() => reset()}
					>
						Not my transfer — disconnect
					</Button>
				</div>
			{:else if currentState === 'declined' || currentState === 'not-chosen'}
				<!-- Ends plainly without calling it an attack: the sender picked or turned away a device. -->
				<div class="grid place-items-center gap-3 py-2 text-center" role="status">
					<div class="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
						<LogOutIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">
							{currentState === 'declined'
								? 'The sender declined this device'
								: 'The sender chose another device'}
						</h4>
						<p class="text-xs text-muted-foreground">
							{currentState === 'declined'
								? "This device received nothing and can't rejoin this code. If you were expecting a secret, ask the sender for a new code."
								: 'This device was disconnected and received nothing. If you were expecting a secret, ask the sender for a new code.'}
						</p>
					</div>
					<Button type="button" variant="outline" class="mt-1 w-full" onclick={() => reset()}>
						Enter a new code
					</Button>
				</div>
			{:else if currentState === 'room-full'}
				<div class="grid place-items-center gap-3 py-2 text-center" role="alert">
					<div class="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
						<UsersIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">Too many devices are trying to connect</h4>
						<p class="text-xs text-muted-foreground">
							Too many devices are trying to connect. Ask the sender for a new code.
						</p>
					</div>
					<Button type="button" variant="outline" class="mt-1 w-full" onclick={() => reset()}>
						Enter a new code
					</Button>
				</div>
			{:else if currentState === 'done-file'}
				<div class="grid place-items-center gap-4 text-center">
					<div class="grid size-12 place-items-center rounded-full bg-emerald-500/15 text-emerald-500">
						<CheckIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-xl">File Downloaded!</h4>
						<p class="text-xs text-muted-foreground">Decrypted and saved directly to your device.</p>
					</div>
					<div class="flex w-full items-center justify-between rounded-xl border border-border bg-muted/40 p-3 text-left">
						<div class="min-w-0 pr-2">
							<p class="truncate text-xs font-semibold text-foreground">{receivedFileName}</p>
							<p class="text-[11px] text-muted-foreground">{formatBytes(receivedFileSize)}</p>
						</div>
						<Button type="button" size="sm" variant="outline" class="gap-1.5" onclick={downloadFile}>
							<DownloadIcon class="size-3.5" aria-hidden="true" />
							Download again
						</Button>
					</div>
					<Button type="button" class="w-full mt-2" onclick={() => { open = false; reset(); }}>
						Done
					</Button>
				</div>
			{:else if currentState === 'done-text'}
				<div class="grid gap-4 text-left">
					<div class="flex items-center justify-between">
						<div class="flex items-center gap-1.5 text-xs font-semibold text-emerald-500">
							<CheckIcon class="size-4" aria-hidden="true" />
							<span>Secret decrypted directly in memory</span>
						</div>
						<span class="micro flex items-center gap-1 text-burn">
							<FlameIcon class="size-3" aria-hidden="true" />
							burned
						</span>
					</div>

					<div class="relative">
						<textarea
							readonly
							aria-label="Decrypted secret content"
							class="min-h-36 w-full rounded-xl border border-border bg-muted/30 p-3 font-mono text-xs text-foreground focus:outline-none"
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
							{copiedText ? 'Copied!' : 'Copy'}
						</Button>
					</div>

					<Button type="button" class="w-full mt-2" onclick={() => { open = false; reset(); }}>
						Done
					</Button>
				</div>
			{:else if currentState === 'error'}
				<div class="grid place-items-center gap-3 py-4 text-center">
					<div class="grid size-12 place-items-center rounded-full bg-destructive/15 text-destructive">
						<XIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-lg text-foreground">Transfer Failed</h4>
						<p class="text-xs text-destructive">{errorMessage}</p>
					</div>
					<Button type="button" variant="outline" class="w-full mt-2" onclick={() => reset()}>
						Try again
					</Button>
				</div>
			{/if}
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
