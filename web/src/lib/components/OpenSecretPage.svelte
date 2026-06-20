<script lang="ts">
import {
	CheckIcon,
	CopyIcon,
	DownloadIcon,
	EyeIcon,
	FileIcon,
	LockKeyholeIcon,
	LockKeyholeOpenIcon
} from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import { resolve } from '$app/paths';
import { createSecretApiClient, SecretApiError, type SecretKind } from '$lib/api/secrets';
import CredentialView from '$lib/components/CredentialView.svelte';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';
import { Button, buttonVariants } from '$lib/components/ui/button';
import * as Card from '$lib/components/ui/card';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
import { Textarea } from '$lib/components/ui/textarea';
import { type CredentialEnvelope, parseCredential } from '$lib/credentials';
import { decodeKeyFragment } from '$lib/crypto/fragment';
import {
	decryptFile,
	decryptFileWithKey,
	decryptText,
	decryptTextWithKey,
	deriveAccessProof,
	type EncryptedFilePayload,
	type EncryptedTextPayload,
	importAesGcmKey
} from '$lib/crypto/text';

type Props = {
	secretId: string;
};

type StatusKind = 'idle' | 'success' | 'error';

let { secretId }: Props = $props();

const api = createSecretApiClient();

let passphrase = $state('');
let openedKind = $state<SecretKind | null>(null);
let decryptedText = $state('');
let credential = $state<CredentialEnvelope | null>(null);
let credentialView = $state<CredentialView | null>(null);
let downloadUrl = $state('');
let downloadFilename = $state('');
let downloadSize = $state(0);
let status = $state('');
let statusKind = $state<StatusKind>('idle');
let isOpening = $state(false);
let hasOpened = $state(false);
let copyState = $state<'idle' | 'copied'>('idle');
// Model A (passphrase) vs Model B (link-bearer). Determined by prefetching
// metadata on mount: Model A secrets expose an access block, Model B do not.
let accessModel = $state<'a' | 'b' | 'unknown'>('unknown');
let linkKey = $state<CryptoKey | null>(null);

onMount(() => {
	void loadModel();
});

async function loadModel(): Promise<void> {
	try {
		const metadata = await api.getSecretMetadata(secretId);
		accessModel = metadata.access ? 'a' : 'b';
		if (accessModel === 'b') {
			const raw = decodeKeyFragment(window.location.hash);
			if (!raw) {
				status = 'This link is incomplete — the decryption key is missing from the URL.';
				statusKind = 'error';
				return;
			}
			linkKey = await importAesGcmKey(raw);
		}
	} catch (error) {
		status = error instanceof SecretApiError ? error.message : 'Could not load this secret.';
		statusKind = 'error';
	}
}

const canOpen = $derived(
	!isOpening && !hasOpened && (accessModel === 'b' ? linkKey !== null : passphrase.length > 0)
);

onDestroy(() => {
	revokeDownloadUrl();
});

function submitOpen(event: SubmitEvent): void {
	event.preventDefault();
	void openSecret();
}

async function openSecret(): Promise<void> {
	if (hasOpened) {
		status = 'Already opened in this tab';
		statusKind = 'success';
		return;
	}
	if (!canOpen) {
		return;
	}

	isOpening = true;
	status = 'Opening';
	statusKind = 'idle';
	decryptedText = '';
	credential = null;
	copyState = 'idle';
	revokeDownloadUrl();

	try {
		if (accessModel === 'b') {
			await openLinkBearerSecret();
		} else {
			await openPassphraseSecret();
		}
		passphrase = '';
		hasOpened = true;
		status = 'Opened';
		statusKind = 'success';
	} catch (error) {
		status = error instanceof SecretApiError ? error.message : 'Could not open this secret.';
		statusKind = 'error';
	} finally {
		isOpening = false;
	}
}

// Model B: the link is the capability. Open without a proof and decrypt with
// the fragment key.
async function openLinkBearerSecret(): Promise<void> {
	const key = linkKey;
	if (!key) {
		throw new Error('missing decryption key');
	}
	const payload = await api.openSecret(secretId);
	if (payload.kind === 'file') {
		const file = await decryptFileWithKey(payload as EncryptedFilePayload, key);
		renderFile(file.bytes, file.filename, file.contentType);
	} else {
		decryptedText = await decryptTextWithKey(payload as EncryptedTextPayload, key);
		credential = parseCredential(decryptedText);
		openedKind = 'text';
	}
}

// Model A: derive an access proof from the passphrase and decrypt with it.
async function openPassphraseSecret(): Promise<void> {
	const metadata = await api.getSecretMetadata(secretId);
	if (!metadata.access) {
		throw new Error('expected access metadata for Model A secret');
	}
	const accessProof = await deriveAccessProof(passphrase, metadata.access.kdf);
	const payload = await api.openSecret(secretId, accessProof);
	if (payload.kind === 'file') {
		const file = await decryptFile(payload as EncryptedFilePayload, passphrase);
		renderFile(file.bytes, file.filename, file.contentType);
	} else {
		decryptedText = await decryptText(payload as EncryptedTextPayload, passphrase);
		credential = parseCredential(decryptedText);
		openedKind = 'text';
	}
}

function renderFile(bytes: Uint8Array, filename: string, contentType: string): void {
	const fileBuffer = bytes.buffer.slice(
		bytes.byteOffset,
		bytes.byteOffset + bytes.byteLength
	) as ArrayBuffer;
	const blob = new Blob([fileBuffer], { type: contentType });
	downloadUrl = URL.createObjectURL(blob);
	downloadFilename = filename;
	downloadSize = bytes.byteLength;
	openedKind = 'file';
}

async function copySecret(): Promise<void> {
	const copyText = credential ? credentialView?.copyText() : decryptedText;
	if (!copyText || copyText.length === 0) {
		return;
	}

	try {
		await navigator.clipboard.writeText(copyText);
		copyState = 'copied';
		window.setTimeout(() => {
			copyState = 'idle';
		}, 1600);
	} catch {
		status = 'Could not copy secret.';
		statusKind = 'error';
	}
}

function revokeDownloadUrl(): void {
	if (downloadUrl.length > 0) {
		URL.revokeObjectURL(downloadUrl);
	}
	downloadUrl = '';
	downloadFilename = '';
	downloadSize = 0;
}

function formatBytes(bytes: number): string {
	if (bytes < 1024) {
		return `${bytes} B`;
	}
	if (bytes < 1024 * 1024) {
		return `${(bytes / 1024).toFixed(1)} KiB`;
	}
	return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
}
</script>

<svelte:head>
	<title>Open secret - Flick</title>
</svelte:head>

<main class="min-h-screen bg-background px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl gap-4">
		<header class="flex items-center justify-between gap-3">
			<a class="inline-flex w-fit items-center gap-2 text-sm font-semibold" href={resolve('/')}>
				<span class="inline-flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
					<LockKeyholeIcon class="size-4" />
				</span>
				<span>Flick</span>
			</a>

			<nav class="flex items-center gap-2">
				<ThemeToggle />
				<a class={buttonVariants({ variant: 'outline', size: 'sm' })} href={resolve('/')}>Create</a>
			</nav>
		</header>

		<section class="grid gap-4">
			<Card.Card class="rounded-lg">
				<Card.Header class="border-b px-4 py-4 sm:px-5">
					<Card.Title class="text-xl">Open secret</Card.Title>
				</Card.Header>
				<Card.Content class="px-4 py-4 sm:px-5">
					<form class="grid gap-4" autocomplete="off" onsubmit={submitOpen}>
						{#if accessModel === 'b'}
							<p class="text-sm text-muted-foreground">
								This link opens without a passphrase. Anyone with the full URL can open it once.
							</p>
						{:else}
							<div class="grid gap-2">
								<Label for="open-passphrase">Passphrase</Label>
								<Input
									id="open-passphrase"
									name="flick-open-passphrase"
									type="password"
									autocomplete="off"
									autocapitalize="none"
									spellcheck="false"
									data-1p-ignore="true"
									data-bwignore="true"
									data-lpignore="true"
									placeholder={hasOpened ? 'Already opened' : 'Required'}
									bind:value={passphrase}
									disabled={isOpening || hasOpened}
									required
								/>
							</div>
						{/if}

						<Button type="submit" class="h-10 w-full" disabled={!canOpen}>
							{#if hasOpened}
								<CheckIcon class="size-4" />
								Opened
							{:else}
								<LockKeyholeOpenIcon class="size-4" />
								{isOpening ? 'Opening' : 'Open'}
							{/if}
						</Button>

						{#if status.length > 0}
							<p
								class="text-sm"
								class:text-muted-foreground={statusKind === 'idle'}
								class:text-emerald-700={statusKind === 'success'}
								class:text-destructive={statusKind === 'error'}
							>
								{status}
							</p>
						{/if}
					</form>
				</Card.Content>
			</Card.Card>

			<Card.Card class="rounded-lg">
				<Card.Header class="border-b px-4 py-4 sm:px-5">
					<div class="flex items-start justify-between gap-3">
						<Card.Title class="text-xl">Secret</Card.Title>
						{#if hasOpened && openedKind === 'text'}
							<Button
								type="button"
								variant="outline"
								size="sm"
								aria-label={credential ? 'Copy all' : 'Copy secret'}
								title={credential ? 'Copy all' : 'Copy secret'}
								onclick={copySecret}
							>
								{#if copyState === 'copied'}
									<CheckIcon class="size-4" />
									Copied
								{:else}
									<CopyIcon class="size-4" />
									{credential ? 'Copy all' : 'Copy'}
								{/if}
							</Button>
						{/if}
					</div>
				</Card.Header>
				<Card.Content class="px-4 py-4 sm:px-5">
					{#if hasOpened && openedKind === 'text' && credential}
						<CredentialView bind:this={credentialView} {credential} />
					{:else if hasOpened && openedKind === 'text'}
						<Textarea
							class="min-h-64 resize-y font-mono text-sm"
							value={decryptedText}
							readonly
							aria-label="Decrypted secret"
						/>
					{:else if hasOpened && openedKind === 'file'}
						<div class="grid min-h-64 place-items-center rounded-lg border border-border bg-muted/30 p-4 text-center">
							<div class="grid max-w-sm justify-items-center gap-4">
								<span class="inline-flex size-12 items-center justify-center rounded-md bg-background text-foreground shadow-xs">
									<FileIcon class="size-6" />
								</span>
								<div class="grid gap-1">
									<p class="break-all text-sm font-medium">{downloadFilename}</p>
									<p class="text-sm text-muted-foreground">{formatBytes(downloadSize)}</p>
								</div>
								<a
									class={buttonVariants({ variant: 'default' })}
									href={downloadUrl}
									download={downloadFilename}
									rel="external"
								>
									<DownloadIcon class="size-4" />
									Download
								</a>
							</div>
						</div>
					{:else}
						<div class="grid min-h-64 place-items-center rounded-lg border border-dashed border-border bg-muted/30 p-4 text-center text-sm text-muted-foreground">
							<div class="grid justify-items-center gap-3">
								<EyeIcon class="size-8" />
								<span>No secret opened</span>
							</div>
						</div>
					{/if}
				</Card.Content>
			</Card.Card>
		</section>
	</div>
</main>
