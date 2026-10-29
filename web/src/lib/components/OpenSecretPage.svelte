<script lang="ts">
import {
	CheckIcon,
	CopyIcon,
	DownloadIcon,
	EyeIcon,
	EyeOffIcon,
	FileIcon,
	FlameIcon,
	LockKeyholeOpenIcon
} from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import { resolve } from '$app/paths';
import {
	createSecretApiClient,
	type GetSecretMetadataResponse,
	type OpenedSecret,
	SecretApiError,
	type SecretKind
} from '$lib/api/secrets';
import CredentialView from '$lib/components/CredentialView.svelte';
import SiteHeader from '$lib/components/SiteHeader.svelte';
import { Button, buttonVariants } from '$lib/components/ui/button';
import * as Card from '$lib/components/ui/card';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
import { Textarea } from '$lib/components/ui/textarea';
import { type CredentialEnvelope, parseCredential } from '$lib/credentials';
import { deriveAccessProof } from '$lib/crypto/access';
import { decodeKeyFragment } from '$lib/crypto/fragment';
import { openFile, openText, unwrapIdentityOffThread } from '$lib/crypto/secret';
import { formatBytes } from '$lib/utils';

type Props = {
	secretId: string;
};

type StatusKind = 'idle' | 'success' | 'error';

let { secretId }: Props = $props();

const api = createSecretApiClient();

let passphrase = $state('');
let revealPassphrase = $state(false);
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
let isLoading = $state(true);
let canRetryLoad = $state(false);
let hasOpened = $state(false);
// True when the link itself is unusable (incomplete / not found / burned) so the only
// sensible next step is to create a new secret. Stays false for passphrase typos,
// which are retryable via the open form.
let linkUnusable = $state(false);
let copyState = $state<'idle' | 'copied'>('idle');
// Model A (passphrase) vs Model B (link-bearer). Determined by prefetching
// metadata on mount: Model A secrets expose an access block, Model B do not.
let accessModel = $state<'a' | 'b' | 'unknown'>('unknown');
let linkIdentity = $state<string | null>(null);
// Metadata loaded on mount or explicit retry and reused on open — the Model A KDF lives here,
// so openPassphraseSecret no longer re-fetches it (one fewer round-trip).
let metadata = $state<GetSecretMetadataResponse | null>(null);
// Set once the server has answered the open: from then on the secret is burned,
// and a decryption failure is final rather than retryable.
let consumedOnServer = false;

onMount(() => {
	void loadModel();
});

async function loadModel(): Promise<void> {
	isLoading = true;
	canRetryLoad = false;
	linkUnusable = false;
	status = 'Loading secret';
	statusKind = 'idle';
	try {
		metadata = await api.getSecretMetadata(secretId);
		accessModel = metadata.access ? 'a' : 'b';
		if (accessModel === 'b') {
			const identity = decodeKeyFragment(window.location.hash);
			if (!identity) {
				status = 'This link is incomplete — the decryption key is missing from the URL.';
				statusKind = 'error';
				linkUnusable = true;
				return;
			}
			linkIdentity = identity;
		}
		status = '';
	} catch (error) {
		status = error instanceof SecretApiError ? error.message : 'Could not load this secret.';
		statusKind = 'error';
		// Missing/burned secrets and unusable fragment keys cannot recover through
		// metadata retry. Network, rate-limit, and server failures can.
		linkUnusable = accessModel === 'b' || isUnavailable(error);
		canRetryLoad = !linkUnusable;
	} finally {
		isLoading = false;
	}
}

function isUnavailable(error: unknown): boolean {
	return error instanceof SecretApiError && (error.status === 404 || error.status === 410);
}

const canOpen = $derived(
	!isLoading &&
		!canRetryLoad &&
		!linkUnusable &&
		!isOpening &&
		!hasOpened &&
		(accessModel === 'b'
			? linkIdentity !== null
			: metadata?.access !== undefined && passphrase.length > 0)
);

let secretHeading = $state<HTMLHeadingElement | null>(null);

// Move focus to the secret heading when the result view replaces the form.
$effect(() => {
	if (hasOpened) {
		secretHeading?.focus();
	}
});

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
		revealPassphrase = false;
		hasOpened = true;
		// The secret is burned server-side; the key is now useless. Drop the
		// reference. ponytail: JS can't securely zero a string/Blob, so this is
		// best-effort reference-dropping for GC, not a memory wipe.
		linkIdentity = null;
	} catch (error) {
		if (consumedOnServer) {
			// The server has already burned the secret, so retrying can only
			// answer "consumed". Say what happened instead.
			status = 'This secret was opened but could not be decrypted. It cannot be opened again.';
			linkUnusable = true;
		} else {
			status = error instanceof SecretApiError ? error.message : 'Could not open this secret.';
			linkUnusable = isUnavailable(error);
		}
		statusKind = 'error';
	} finally {
		isOpening = false;
	}
}

// Model B: the link is the capability. Open without a proof and decrypt with
// the fragment identity.
async function openLinkBearerSecret(): Promise<void> {
	const identity = linkIdentity;
	if (!identity) {
		throw new Error('missing decryption key');
	}
	const opened = await api.openSecret(secretId);
	consumedOnServer = true;
	await render(identity, opened);
}

// Model A: the passphrase-derived proof opens the secret, and the same
// passphrase unwraps the identity the response carries. Reuses the metadata
// fetched on mount instead of a second round-trip.
async function openPassphraseSecret(): Promise<void> {
	if (!metadata?.access) {
		throw new Error('expected access metadata for Model A secret');
	}
	const accessProof = await deriveAccessProof(passphrase, metadata.access.kdf);
	const opened = await api.openSecret(secretId, accessProof);
	consumedOnServer = true;
	if (!opened.wrappedIdentity) {
		throw new Error('expected a wrapped identity for Model A secret');
	}
	await render(await unwrapIdentityOffThread(opened.wrappedIdentity, passphrase), opened);
}

async function render(identity: string, opened: OpenedSecret): Promise<void> {
	if (opened.kind === 'file') {
		if (!opened.encryptedFilename) {
			throw new Error('expected an encrypted filename for a file secret');
		}
		const file = await openFile(identity, opened.body, opened.encryptedFilename);
		downloadUrl = URL.createObjectURL(file.blob);
		downloadFilename = file.filename;
		downloadSize = file.blob.size;
		openedKind = 'file';
	} else {
		decryptedText = await openText(identity, opened.body);
		credential = parseCredential(decryptedText);
		openedKind = 'text';
	}
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
</script>

<svelte:head>
	<title>Open secret - Flick</title>
</svelte:head>

<main class="min-h-screen px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl gap-8">
		<SiteHeader link={{ href: resolve('/'), label: 'Create' }} />

		{#if hasOpened}
			<section class="grid gap-6">
				<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-success">
						<CheckIcon class="size-3.5" aria-hidden="true" />
						decrypted · burns after read
					</p>
					<h1
						class="page-title"
						tabindex="-1"
						bind:this={secretHeading}
					>
						Secret
					</h1>
				</div>

				{#if openedKind === 'text' && credential}
					<div class="grid gap-4">
						<CredentialView bind:this={credentialView} {credential} />
						<Button
							type="button"
							variant="outline"
							class="h-11 w-full"
							aria-label="Copy all"
							onclick={() => {
								void copySecret();
							}}
						>
							{#if copyState === 'copied'}
								<CheckIcon class="size-4" aria-hidden="true" />
								Copied
							{:else}
								<CopyIcon class="size-4" aria-hidden="true" />
								Copy all
							{/if}
						</Button>
					</div>
				{:else if openedKind === 'text'}
					<div class="grid gap-3">
						<Textarea
							class="min-h-64 resize-y font-mono text-sm"
							value={decryptedText}
							readonly
							aria-label="Decrypted secret"
						/>
						<Button
							type="button"
							variant="outline"
							class="h-11 w-full"
							aria-label="Copy secret"
							onclick={() => {
								void copySecret();
							}}
						>
							{#if copyState === 'copied'}
								<CheckIcon class="size-4" aria-hidden="true" />
								Copied
							{:else}
								<CopyIcon class="size-4" aria-hidden="true" />
								Copy
							{/if}
						</Button>
					</div>
				{:else if openedKind === 'file'}
					<Card.Root class="overflow-hidden">
						<Card.Content class="grid place-items-center gap-4 p-6 text-center">
							<div class="grid max-w-sm justify-items-center gap-4">
								<span
									class="grid size-12 place-items-center rounded-lg bg-background text-foreground shadow-xs"
								>
									<FileIcon class="size-6" aria-hidden="true" />
								</span>
								<div class="grid gap-1">
									<p class="break-all text-sm font-medium">{downloadFilename}</p>
									<p class="font-mono text-xs text-muted-foreground">
										{formatBytes(downloadSize)}
									</p>
								</div>
								<a
									class={buttonVariants({ size: 'cta' })}
									href={downloadUrl}
									download={downloadFilename}
									rel="external"
								>
									<DownloadIcon class="size-4" aria-hidden="true" />
									Download
								</a>
							</div>
						</Card.Content>
					</Card.Root>
				{/if}

				{#if status.length > 0}
					<p
						class="text-sm"
						class:text-muted-foreground={statusKind === 'idle'}
						class:text-success={statusKind === 'success'}
						class:text-destructive={statusKind === 'error'}
						role={statusKind === 'error' ? 'alert' : 'status'}
						aria-live={statusKind === 'error' ? 'assertive' : 'polite'}
					>
						{status}
					</p>
				{/if}

				<a
					class={buttonVariants({ variant: 'ghost' }) + ' h-9 w-full text-sm'}
					href={resolve('/')}
					aria-label="Create your own secret"
				>
					Create your own
				</a>
			</section>
		{:else}
			<section class="grid gap-7">
				<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						one-time link
					</p>
					<h1 class="page-title">Open a secret</h1>
				</div>

				<form class="grid gap-5" autocomplete="off" onsubmit={submitOpen}>
					{#if accessModel === 'b'}
						<p class="text-sm text-muted-foreground">
							This link opens without a passphrase. Anyone with the full URL can open it once.
						</p>
					{:else if accessModel === 'a'}
						<div class="grid gap-2.5">
							<Label for="open-passphrase" class="micro font-normal text-muted-foreground">
								passphrase
							</Label>
							<div class="relative">
								<Input
									id="open-passphrase"
									name="flick-open-passphrase"
									type={revealPassphrase ? 'text' : 'password'}
									autocomplete="off"
									autocapitalize="none"
									spellcheck="false"
									data-1p-ignore="true"
									data-bwignore="true"
									data-lpignore="true"
									class="h-11 pr-11"
									placeholder={hasOpened ? 'Already opened' : 'Required'}
									bind:value={passphrase}
									disabled={isOpening || hasOpened}
									required
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									class="absolute right-0 top-0 size-11 text-muted-foreground hover:text-foreground"
									aria-label={revealPassphrase ? 'Hide passphrase' : 'Show passphrase'}
									title={revealPassphrase ? 'Hide passphrase' : 'Show passphrase'}
									onclick={() => {
										revealPassphrase = !revealPassphrase;
									}}
								>
									{#if revealPassphrase}
										<EyeOffIcon class="size-4" aria-hidden="true" />
									{:else}
										<EyeIcon class="size-4" aria-hidden="true" />
									{/if}
								</Button>
							</div>
						</div>
					{/if}

					<Button type="submit" size="cta" disabled={!canOpen}>
						{#if hasOpened}
							<CheckIcon class="size-4" aria-hidden="true" />
							Opened
						{:else}
							<LockKeyholeOpenIcon class="size-4" aria-hidden="true" />
							{isOpening ? 'Opening' : 'Open'}
						{/if}
					</Button>

					{#if canRetryLoad}
						<Button type="button" variant="outline" class="h-11 w-full" onclick={() => void loadModel()}>
							Retry loading
						</Button>
					{/if}

					{#if status.length > 0}
						<p
							class="text-sm"
							class:text-muted-foreground={statusKind === 'idle'}
							class:text-success={statusKind === 'success'}
							class:text-destructive={statusKind === 'error'}
							role={statusKind === 'error' ? 'alert' : 'status'}
							aria-live={statusKind === 'error' ? 'assertive' : 'polite'}
						>
							{status}
						</p>
					{/if}
				</form>

				{#if linkUnusable && statusKind === 'error'}
					<a
						class={buttonVariants({ variant: 'ghost' }) + ' h-9 w-full text-sm'}
						href={resolve('/')}
						aria-label="Create a new secret instead"
					>
						Create one instead
					</a>
				{/if}
			</section>
		{/if}
	</div>
</main>
