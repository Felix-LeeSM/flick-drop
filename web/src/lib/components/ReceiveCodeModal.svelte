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
import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
import {
	computePipePassphrase,
	decryptPipeStream,
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
};

let { open = $bindable(false), apiBaseUrl = '' }: Props = $props();

type ReceiveState =
	| 'input'
	| 'connecting'
	| 'waiting-approval'
	| 'streaming'
	| 'done-file'
	| 'done-text'
	| 'error';

let rawCode = $state('');
let optionalPassphrase = $state('');
let showPassphraseField = $state(false);

let currentState = $state<ReceiveState>('input');
let progressPercent = $state(0);
let errorMessage = $state('');
let statusMessage = $state('');
let matchCode = $state('482');

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
}

$effect(() => {
	if (!open) {
		reset();
	}
});

function handleCodeInput(event: Event): void {
	const input = event.target as HTMLInputElement;
	// Extract digits only, up to 6 digits
	const digits = input.value.replace(/\D/g, '').slice(0, 6);
	rawCode = digits;

	if (digits.length === 6) {
		// Auto-submit when 6 digits are complete
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

	currentState = 'connecting';
	statusMessage = 'Connecting to transfer pipe...';
	errorMessage = '';
	abortController = new AbortController();

	try {
		// Attempt real receive if apiBaseUrl is available
		if (apiBaseUrl) {
			const result = await receivePipeStream(apiBaseUrl, normalized, fetch, abortController.signal);

			currentState = 'streaming';
			statusMessage = 'Decrypting live age stream...';

			const derivedPassphrase = computePipePassphrase(normalized, result.salt, optionalPassphrase);
			const decryptedStream = await decryptPipeStream(result.stream, derivedPassphrase);

			if (result.kind === 'file') {
				const bytes = await streamToBytes(decryptedStream);
				receivedFileSize = bytes.length;
				receivedFileName = result.encryptedFilename ?? 'downloaded-file';
				receivedFileBlob = new Blob([bytes as Uint8Array<ArrayBuffer>]);
				currentState = 'done-file';
				downloadFile();
			} else {
				receivedText = await streamToText(decryptedStream);
				currentState = 'done-text';
			}
		} else {
			// Demo simulation mode when running without backend
			await simulateDemoReceive(normalized);
		}
	} catch (err) {
		if (abortController.signal.aborted) {
			return;
		}
		currentState = 'error';
		errorMessage = err instanceof Error ? err.message : 'Transfer failed';
	}
}

async function simulateDemoReceive(code: string): Promise<void> {
	currentState = 'connecting';
	statusMessage = `Connecting to pipe ${formatPipeCode(code)}...`;
	await new Promise((r) => setTimeout(r, 600));

	// If code is 741928, simulate 2-step SAS waiting for sender approval
	if (code === '741928') {
		currentState = 'waiting-approval';
		matchCode = '482';
		return;
	}

	await continueStreamingAfterApproval(code);
}

async function continueStreamingAfterApproval(code = rawCode): Promise<void> {
	currentState = 'streaming';
	statusMessage = 'Authenticating 64 KiB chunks on-the-fly...';
	for (let i = 20; i <= 100; i += 20) {
		progressPercent = i;
		await new Promise((r) => setTimeout(r, 150));
	}

	// If code ends with 0, simulate file; otherwise simulate secret text
	if (code.endsWith('0')) {
		receivedFileName = 'quarterly-audit-report-2026.pdf';
		receivedFileSize = 2_840_000;
		receivedFileBlob = new Blob(['Mock PDF binary stream content for prototype'], {
			type: 'application/pdf'
		});
		currentState = 'done-file';
	} else {
		receivedText =
			`DEMO-SECRET-PAYLOAD-FOR-CODE-${formatPipeCode(code)}\n\n` +
			'API_TOKEN=flick_live_pipe_b73e89a42\n' +
			'DATABASE_URL=postgres://app:ephemeral@10.0.4.12:5432/live\n' +
			'Decrypted directly in browser memory without intermediate storage.';
		currentState = 'done-text';
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
		// Fallback ignored in prototype
	}
}
</script>

<Dialog.Root bind:open={open}>
	<Dialog.Portal>
		<Dialog.Overlay
			class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0"
		/>
		<Dialog.Content
			class="fixed left-1/2 top-1/2 z-50 grid w-[23rem] max-w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border bg-card p-6 text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<div class="relative flex items-center justify-between">
				<div class="flex items-center gap-2">
					<span class="grid size-6 place-items-center rounded-md bg-primary/10 text-primary">
						<ZapIcon class="size-3.5" aria-hidden="true" />
					</span>
					<Dialog.Title class="font-serif text-xl tracking-tight">Receive Code Drop</Dialog.Title>
				</div>
				<Dialog.Close
					class="grid size-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
					aria-label="Close"
				>
					<XIcon class="size-4" aria-hidden="true" />
				</Dialog.Close>
			</div>

			<Dialog.Description class="sr-only">
				Enter a 6-digit code to connect to the sender's live memory pipe and receive encrypted data.
			</Dialog.Description>

			{#if currentState === 'input'}
				<div class="grid gap-4 text-center">
					<p class="text-xs text-muted-foreground">
						Enter the 6-digit number displayed on the sender's screen:
					</p>

					<!-- Visual Code Input Formatter -->
					<div class="grid gap-2">
						<div class="relative flex justify-center">
							<input
								id="pipe-code-input"
								type="text"
								inputmode="numeric"
								pattern="[0-9]*"
								maxlength="6"
								value={rawCode}
								oninput={handleCodeInput}
								placeholder="000000"
								class="h-14 w-full rounded-xl border-2 border-primary/40 bg-muted/30 text-center font-mono text-2xl font-bold tracking-[0.4em] text-foreground transition-all focus:border-primary focus:bg-background focus:outline-none focus:ring-4 focus:ring-primary/10 sm:text-3xl"
							/>
						</div>
						<p class="text-[11px] text-muted-foreground">
							Auto-connects immediately when 6 digits are typed.
						</p>
					</div>

					<!-- Optional passphrase drawer -->
					<div class="border-t border-border/60 pt-2 text-left">
						<button
							type="button"
							class="text-[11px] text-muted-foreground hover:text-foreground transition underline underline-offset-2"
							onclick={() => {
								showPassphraseField = !showPassphraseField;
							}}
						>
							{showPassphraseField ? 'Hide secret passphrase' : 'Has a secret passphrase?'}
						</button>
						{#if showPassphraseField}
							<div class="mt-2">
								<input
									type="password"
									bind:value={optionalPassphrase}
									placeholder="Optional sender passphrase"
									class="h-9 w-full rounded-lg border border-input bg-muted/30 px-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
								/>
							</div>
						{/if}
					</div>

					<button
						type="button"
						class={cn(buttonVariants({ size: 'default' }), 'w-full gap-2 font-semibold')}
						disabled={rawCode.length !== 6}
						onclick={() => startReceive()}
					>
						<ZapIcon class="size-4" aria-hidden="true" />
						Connect & Receive
					</button>

					<!-- Quick simulation helper for prototype reviewers -->
					<div class="rounded-lg bg-muted/40 p-2 text-[11px] text-muted-foreground">
						<span>💡 Review test: </span>
						<button
							type="button"
							class="underline hover:text-primary"
							onclick={() => {
								rawCode = '741928';
								void startReceive();
							}}
						>
							Code: 741928 (Text)
						</button>
						<span> | </span>
						<button
							type="button"
							class="underline hover:text-primary"
							onclick={() => {
								rawCode = '882140';
								void startReceive();
							}}
						>
							Code: 882140 (File)
						</button>
					</div>
				</div>
			{:else if currentState === 'waiting-approval'}
				<div class="grid place-items-center gap-4 py-3 text-center animate-in fade-in zoom-in-95">
					<div class="grid size-12 place-items-center rounded-full bg-primary/15 text-primary">
						<ShieldCheckIcon class="size-6" aria-hidden="true" />
					</div>

					<div class="space-y-1">
						<h4 class="font-serif text-lg font-semibold text-foreground">Waiting for sender approval</h4>
						<p class="text-xs text-muted-foreground">
							Verify that this code matches the sender's screen:
						</p>
					</div>

					<!-- Big Match Code -->
					<div class="w-full rounded-xl border border-primary/30 bg-primary/5 p-3">
						<span class="text-[10px] font-mono uppercase tracking-wider text-muted-foreground">Confirmation Code</span>
						<div class="font-mono text-3xl font-extrabold text-primary tracking-widest pt-0.5">
							{matchCode}
						</div>
					</div>

					<div class="flex items-center gap-2 text-xs text-muted-foreground">
						<div class="size-2 rounded-full bg-primary animate-ping"></div>
						<span>Transfer begins automatically once approved by the sender.</span>
					</div>

					<div class="w-full pt-2 space-y-2">
						<Button
							type="button"
							class="w-full h-10 font-bold bg-primary text-primary-foreground hover:bg-primary/90 text-xs shadow-sm"
							onclick={() => continueStreamingAfterApproval()}
						>
							Simulate sender approval
						</Button>
						<button
							type="button"
							class="text-xs text-muted-foreground hover:text-foreground underline underline-offset-2"
							onclick={() => reset()}
						>
							Cancel
						</button>
					</div>
				</div>
			{:else if currentState === 'connecting' || currentState === 'streaming'}
				<div class="grid place-items-center gap-4 py-4 text-center">
					<div class="size-10 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
					<div class="grid gap-1">
						<h4 class="font-medium text-sm text-foreground">{statusMessage}</h4>
						<p class="font-mono text-xs text-muted-foreground">Pipe: {formatPipeCode(rawCode)}</p>
					</div>
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
					<button
						type="button"
						class="text-xs text-muted-foreground hover:text-foreground"
						onclick={() => reset()}
					>
						Cancel
					</button>
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
						<button
							type="button"
							class={cn(buttonVariants({ variant: 'outline', size: 'sm' }), 'shrink-0 gap-1.5')}
							onclick={() => downloadFile()}
						>
							<DownloadIcon class="size-3.5" aria-hidden="true" />
							Save again
						</button>
					</div>
					<p class="flex items-center gap-1.5 text-[11px] text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						In-memory stream burned. Cannot be opened again.
					</p>
					<Dialog.Close class={cn(buttonVariants({ variant: 'default' }), 'w-full')}>
						Done
					</Dialog.Close>
				</div>
			{:else if currentState === 'done-text'}
				<div class="grid gap-4 text-center">
					<div class="mx-auto grid size-12 place-items-center rounded-full bg-emerald-500/15 text-emerald-500">
						<CheckIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-serif text-xl">Secret Received</h4>
						<p class="text-xs text-muted-foreground">Decrypted directly in browser memory.</p>
					</div>
					<div class="max-h-48 overflow-y-auto rounded-xl border border-border bg-muted/50 p-3 text-left font-mono text-xs text-foreground whitespace-pre-wrap break-all">
						{receivedText}
					</div>
					<button
						type="button"
						class={cn(buttonVariants({ variant: 'outline' }), 'w-full gap-2')}
						onclick={() => copyText()}
					>
						<CopyIcon class="size-4" aria-hidden="true" />
						{copiedText ? 'Copied to clipboard!' : 'Copy secret text'}
					</button>
					<p class="flex items-center justify-center gap-1.5 text-[11px] text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						Burned from server memory. Close tab when finished.
					</p>
					<Dialog.Close class={cn(buttonVariants({ variant: 'default' }), 'w-full')}>
						Close
					</Dialog.Close>
				</div>
			{:else if currentState === 'error'}
				<div class="grid place-items-center gap-4 text-center">
					<div class="grid size-12 place-items-center rounded-full bg-destructive/15 text-destructive">
						<XIcon class="size-6" aria-hidden="true" />
					</div>
					<div class="grid gap-1">
						<h4 class="font-semibold text-sm text-foreground">Transfer Failed</h4>
						<p class="text-xs text-destructive">{errorMessage}</p>
					</div>
					<p class="text-[11px] text-muted-foreground">
						Codes expire in 3 minutes or burn after 3 failed attempts to protect against brute-force attacks.
					</p>
					<button
						type="button"
						class={cn(buttonVariants({ variant: 'default' }), 'w-full')}
						onclick={() => {
							currentState = 'input';
						}}
					>
						Try another code
					</button>
				</div>
			{/if}
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>
