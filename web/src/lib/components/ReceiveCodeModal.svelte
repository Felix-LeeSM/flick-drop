<script lang="ts">
import {
	CheckIcon,
	CopyIcon,
	DownloadIcon,
	FlameIcon,
	LockKeyholeIcon,
	ShieldCheckIcon,
	XIcon,
	ZapIcon
} from '@lucide/svelte';
import { Dialog } from 'bits-ui';
import { DEFAULT_API_BASE_URL } from '#lib/api/secrets.js';
import { Button } from '#lib/components/ui/button/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import {
	computePipePassphrase,
	decryptPipeStream,
	deriveConfirmationCode,
	formatPipeCode,
	normalizePipeCode,
	receivePipeStream,
	streamToBytes,
	streamToText
} from '#lib/crypto/pipe.js';
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

type ReceiveState = 'input' | 'connecting' | 'streaming' | 'done-file' | 'done-text' | 'error';

let rawCode = $state('');
let codeFocused = $state(false);
let optionalPassphrase = $state('');
let showPassphraseField = $state(false);

let currentState = $state<ReceiveState>('input');
// Entering the code is step 1; waiting on the sender's confirmation and the transfer is step 2.
const step = $derived(
	currentState === 'input'
		? 1
		: currentState === 'connecting' || currentState === 'streaming'
			? 2
			: undefined
);
let progressPercent = $state(0);
let errorMessage = $state('');
let statusMessage = $state('');
let matchCode = $state('');

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
	optionalPassphrase = '';
	showPassphraseField = false;
	currentState = 'input';
	progressPercent = 0;
	errorMessage = '';
	statusMessage = '';
	receivedText = '';
	receivedFileName = '';
	receivedFileSize = 0;
	receivedFileBlob = null;
	copiedText = false;
	matchCode = '';
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
	const sanitized = target.value.replace(/\D/g, '').slice(0, 6);
	rawCode = sanitized;

	if (sanitized.length === 6) {
		void startReceive();
	}
}

async function startReceive(): Promise<void> {
	let normalized: string;
	try {
		normalized = normalizePipeCode(rawCode);
	} catch {
		errorMessage = 'Please enter a valid 6-digit number';
		currentState = 'error';
		return;
	}

	matchCode = deriveConfirmationCode(normalized);
	currentState = 'connecting';
	statusMessage = 'Connecting to transfer pipe...';
	errorMessage = '';
	abortController = new AbortController();

	try {
		const targetBaseUrl = apiBaseUrl || DEFAULT_API_BASE_URL;
		const result = await receivePipeStream(
			targetBaseUrl,
			normalized,
			fetch,
			abortController.signal
		);

		currentState = 'streaming';
		statusMessage = 'Decrypting live stream...';

		const derivedPassphrase = computePipePassphrase(normalized, result.salt, optionalPassphrase);
		const decryptedStream = await decryptPipeStream(result.stream, derivedPassphrase);

		if (result.kind === 'file') {
			const bytes = await streamToBytes(decryptedStream);
			receivedFileSize = bytes.length;
			if (result.encryptedFilename) {
				try {
					const encBytes = Uint8Array.from(atob(result.encryptedFilename), (c) => c.charCodeAt(0));
					receivedFileName = await streamToText(
						await decryptPipeStream(encBytes, derivedPassphrase)
					);
				} catch {
					receivedFileName = 'downloaded-file';
				}
			} else {
				receivedFileName = 'downloaded-file';
			}
			receivedFileBlob = new Blob([bytes as Uint8Array<ArrayBuffer>]);
			currentState = 'done-file';
			downloadFile();
		} else {
			receivedText = await streamToText(decryptedStream);
			currentState = 'done-text';
		}
	} catch (err) {
		if (abortController.signal.aborted) {
			return;
		}
		currentState = 'error';
		errorMessage = err instanceof Error ? err.message : 'Transfer failed';
	}
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
							{step === 1 ? 'Connect to sender' : "Check sender's screen"}
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

			<!-- The dialog keeps one name in every step; the visible heading names the step. -->
			<Dialog.Title class="sr-only">Receive secret with code</Dialog.Title>
			<Dialog.Description class="sr-only">
				Enter a 6-digit code to connect to the sender's live memory pipe and receive encrypted data.
			</Dialog.Description>

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
										codeFocused && slot === Math.min(rawCode.length, 5) && 'ring-3 ring-ring/50'
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
								maxlength="6"
								autocomplete="one-time-code"
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

					<!-- Optional passphrase drawer -->
					<div class="text-left">
						<button
							type="button"
							class="text-xs text-muted-foreground underline underline-offset-2 transition-colors hover:text-foreground"
							aria-expanded={showPassphraseField}
							onclick={() => {
								showPassphraseField = !showPassphraseField;
							}}
						>
							{showPassphraseField ? 'Hide additional passphrase' : 'Has an additional passphrase?'}
						</button>
						{#if showPassphraseField}
							<div class="mt-2">
								<Label for="pipe-passphrase" class="sr-only">Additional passphrase</Label>
								<input
									id="pipe-passphrase"
									type="password"
									bind:value={optionalPassphrase}
									placeholder="Sender's additional passphrase"
									class="h-10 w-full rounded-lg border border-input bg-muted/30 px-3 font-mono text-base text-foreground placeholder:font-sans placeholder:text-muted-foreground focus:border-primary focus:outline-none md:text-sm"
								/>
							</div>
						{/if}
					</div>

					<div class="grid gap-2">
						<Button type="button" size="cta" disabled={rawCode.length !== 6} onclick={() => startReceive()}>
							<ZapIcon class="size-4" aria-hidden="true" />
							Verify & connect
						</Button>
						<p class="text-[0.6875rem] text-muted-foreground">
							Next: you'll verify a 3-digit match code with the sender.
						</p>
					</div>
				</div>
			{:else if currentState === 'connecting' || currentState === 'streaming'}
				<div class="grid place-items-center gap-4 text-center">
					<div class="grid gap-1">
						<div class="flex items-center justify-center gap-2.5">
							<div
								class="size-4 animate-spin rounded-full border-2 border-primary border-t-transparent motion-reduce:animate-none"
								aria-hidden="true"
							></div>
							<h4 class="text-sm font-medium text-foreground">{statusMessage}</h4>
						</div>
						<p class="font-mono text-xs text-muted-foreground">Pipe: {formatPipeCode(rawCode)}</p>
					</div>

					{#if matchCode}
						<div class="w-full rounded-2xl border border-primary/35 bg-primary/8 p-4">
							<span class="micro text-muted-foreground">Confirmation Code</span>
							<div class="pt-1 font-mono text-4xl font-extrabold tracking-widest text-primary">
								{matchCode}
							</div>
						</div>
						<p class="text-[11px] text-muted-foreground">
							Verify this code matches the sender's screen.
						</p>
					{/if}

					{#if currentState === 'streaming'}
						<div class="w-full space-y-1">
							<div class="h-2 w-full overflow-hidden rounded-full bg-muted">
								<div
									class="h-full bg-primary transition-all duration-200"
									style="width: {progressPercent}%"
								></div>
							</div>
							<p class="text-[11px] font-mono text-muted-foreground">{progressPercent}% received</p>
						</div>
					{/if}
					<Button
						type="button"
						variant="ghost"
						size="sm"
						class="text-xs text-muted-foreground hover:text-foreground"
						onclick={() => reset()}
					>
						Cancel
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
