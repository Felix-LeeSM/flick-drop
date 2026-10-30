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
import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
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
let optionalPassphrase = $state('');
let showPassphraseField = $state(false);

let currentState = $state<ReceiveState>('input');
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
			class="fixed left-1/2 top-1/2 z-50 grid w-[23rem] max-w-[calc(100vw-1.5rem)] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border bg-card p-6 text-card-foreground shadow-2xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95"
		>
			<div class="relative flex items-center justify-between">
				<div class="flex items-center gap-2">
					<span class="grid size-6 place-items-center rounded-md bg-primary/10 text-primary">
						<ZapIcon class="size-3.5" aria-hidden="true" />
					</span>
					<Dialog.Title class="font-serif text-xl tracking-tight">Receive secret with code</Dialog.Title>
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
						<Label for="pipe-code-input" class="sr-only">6-digit transfer code</Label>
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
								<Label for="pipe-passphrase" class="sr-only">Optional secret passphrase</Label>
								<input
									id="pipe-passphrase"
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
						Connect & receive
					</button>
				</div>
			{:else if currentState === 'connecting' || currentState === 'streaming'}
				<div class="grid place-items-center gap-4 py-4 text-center">
					<div class="size-10 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
					<div class="grid gap-1">
						<h4 class="font-medium text-sm text-foreground">{statusMessage}</h4>
						<p class="font-mono text-xs text-muted-foreground">Pipe: {formatPipeCode(rawCode)}</p>
					</div>

					{#if matchCode}
						<div class="w-full rounded-xl border border-primary/30 bg-primary/5 p-3">
							<span class="text-[10px] font-mono uppercase tracking-wider text-muted-foreground">Confirmation Code</span>
							<div class="font-mono text-3xl font-extrabold text-primary tracking-widest pt-0.5">
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
