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
import { onDestroy, onMount, tick } from 'svelte';
import {
	createSecretApiClient,
	type GetSecretMetadataResponse,
	type OpenedSecret,
	SecretApiError,
	type SecretKind
} from '#lib/api/secrets.js';
import CredentialView from '#lib/components/CredentialView.svelte';
import LockStageCard from '#lib/components/LockStageCard.svelte';
import SiteHeader from '#lib/components/SiteHeader.svelte';
import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
import * as Card from '#lib/components/ui/card/index.js';
import { Input } from '#lib/components/ui/input/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import { Textarea } from '#lib/components/ui/textarea/index.js';
import { type CredentialEnvelope, parseCredential } from '#lib/credentials/index.js';
import { deriveAccessProof } from '#lib/crypto/access.js';
import { recipientOf } from '#lib/crypto/age.js';
import { decodeKeyFragment } from '#lib/crypto/fragment.js';
import { openFile, openText, unwrapIdentityOffThread } from '#lib/crypto/secret.js';
import { t } from '#lib/i18n/index.js';
import {
	holdMinimumStage,
	STAGE_FAIL_MS,
	STAGE_FINISH_MS,
	type StagePhase,
	sleep
} from '#lib/stage-timing.js';
import { formatBytes } from '#lib/utils.js';
import { resolve } from '$app/paths';

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
let openStage = $state<StagePhase | null>(null);
let isLoading = $state(true);
let canRetryLoad = $state(false);
let hasOpened = $state(false);
// The link itself is unusable (incomplete, missing, burned); passphrase typos stay retryable.
let linkUnusable = $state(false);
let copyState = $state<'idle' | 'copied'>('idle');
// Model A metadata carries an access block; Model B does not.
let accessModel = $state<'a' | 'b' | 'unknown'>('unknown');
let linkIdentity = $state<string | null>(null);
let metadata = $state<GetSecretMetadataResponse | null>(null);
// Set once the server answered the open: the secret is burned, so a decrypt failure is final.
let consumedOnServer = false;
// The open body stream dropped, as opposed to the bytes failing to decrypt.
let transportBroken = false;

onMount(() => {
	void loadModel();
});

async function loadModel(): Promise<void> {
	isLoading = true;
	canRetryLoad = false;
	linkUnusable = false;
	status = t().open.loading;
	statusKind = 'idle';
	try {
		metadata = await api.getSecretMetadata(secretId);
		accessModel = metadata.access ? 'a' : 'b';
		if (accessModel === 'b') {
			const identity = decodeKeyFragment(window.location.hash);
			if (!identity) {
				status = t().open.linkIncomplete;
				statusKind = 'error';
				linkUnusable = true;
				return;
			}
			// Catch a cut or altered link by its checksum before Open burns the secret.
			if (!(await isIntactKey(identity))) {
				status = t().open.linkDamaged;
				statusKind = 'error';
				linkUnusable = true;
				return;
			}
			linkIdentity = identity;
		}
		status = '';
	} catch (error) {
		status = error instanceof SecretApiError ? error.message : t().open.loadFailed;
		statusKind = 'error';
		// Only network, rate-limit, and server failures recover through metadata retry.
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

// A Model B key here already passed isIntactKey, so a decrypt failure means damaged ciphertext.
function consumedFailureMessage(): string {
	if (transportBroken) {
		return t().open.transportDropped;
	}
	return t().open.decryptFailed;
}

async function openSecret(): Promise<void> {
	if (hasOpened) {
		status = t().open.alreadyOpened;
		statusKind = 'success';
		return;
	}
	if (!canOpen) {
		return;
	}

	isOpening = true;
	openStage = 'waiting';
	const stageStartedAt = performance.now();
	status = t().open.statusOpening;
	statusKind = 'idle';
	decryptedText = '';
	credential = null;
	copyState = 'idle';
	revokeDownloadUrl();
	transportBroken = false;

	try {
		if (accessModel === 'b') {
			await openLinkBearerSecret();
		} else {
			await openPassphraseSecret();
		}
		await holdMinimumStage(stageStartedAt);
		openStage = 'finishing';
		await sleep(STAGE_FINISH_MS);
		passphrase = '';
		revealPassphrase = false;
		hasOpened = true;
		// Best-effort: JS cannot zero a string/Blob, so only drop the reference for GC.
		linkIdentity = null;
	} catch (error) {
		if (consumedOnServer) {
			status = consumedFailureMessage();
			linkUnusable = true;
		} else {
			status = error instanceof SecretApiError ? error.message : t().open.openFailed;
			linkUnusable = isUnavailable(error);
		}
		statusKind = 'error';
		await holdMinimumStage(stageStartedAt);
		openStage = 'failing';
		await sleep(STAGE_FAIL_MS);
	} finally {
		isOpening = false;
		openStage = null;
	}
	if (statusKind === 'error') {
		await refocusForm();
	}
}

// The stage card took the focused control with it; refocus where a retry starts.
async function refocusForm(): Promise<void> {
	await tick();
	document
		.querySelector<HTMLElement>(
			'#open-passphrase:not(:disabled), form button[type="submit"]:not(:disabled)'
		)
		?.focus();
}

// Model B: the link is the capability; open without a proof.
async function openLinkBearerSecret(): Promise<void> {
	const identity = linkIdentity;
	if (!identity) {
		throw new Error('missing decryption key');
	}
	const opened = await api.openSecret(secretId);
	consumedOnServer = true;
	await render(identity, opened);
}

// Model A: the passphrase-derived proof opens it; the same passphrase unwraps the identity.
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

// The bech32 checksum inside the key detects a cut or altered link.
async function isIntactKey(identity: string): Promise<boolean> {
	try {
		await recipientOf(identity);
		return true;
	} catch {
		return false;
	}
}

function watchTransport(body: ReadableStream<Uint8Array>): ReadableStream<Uint8Array> {
	const reader = body.getReader();
	return new ReadableStream({
		async pull(controller) {
			try {
				const { done, value } = await reader.read();
				if (done) {
					controller.close();
				} else {
					controller.enqueue(value);
				}
			} catch (error) {
				transportBroken = true;
				controller.error(error);
			}
		},
		cancel(reason) {
			return reader.cancel(reason);
		}
	});
}

async function render(identity: string, opened: OpenedSecret): Promise<void> {
	const body = watchTransport(opened.body);
	if (opened.kind === 'file') {
		if (!opened.encryptedFilename) {
			throw new Error('expected an encrypted filename for a file secret');
		}
		const file = await openFile(identity, body, opened.encryptedFilename);
		downloadUrl = URL.createObjectURL(file.blob);
		downloadFilename = file.filename;
		downloadSize = file.blob.size;
		openedKind = 'file';
	} else {
		decryptedText = await openText(identity, body);
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
		status = t().open.copyFailed;
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
	<title>{t().open.headTitle} - Flick</title>
</svelte:head>

<main class="min-h-screen px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl grid-cols-[minmax(0,1fr)] gap-8">
		<SiteHeader link={{ href: resolve('/'), label: t().open.headerCreate }} />

		{#if hasOpened}
			<section class="grid gap-6">
				<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-success">
						<CheckIcon class="size-3.5" aria-hidden="true" />
						{t().open.decryptedBadge}
					</p>
					<h1
						class="page-title"
						tabindex="-1"
						bind:this={secretHeading}
					>
						{t().open.resultTitle}
					</h1>
				</div>

				{#if openedKind === 'text' && credential}
					<div class="grid gap-4">
						<CredentialView bind:this={credentialView} {credential} />
						<Button
							type="button"
							variant="outline"
							class="h-11 w-full"
							aria-label={t().open.copyAll}
							onclick={() => {
								void copySecret();
							}}
						>
							{#if copyState === 'copied'}
								<CheckIcon class="size-4" aria-hidden="true" />
								{t().open.copied}
							{:else}
								<CopyIcon class="size-4" aria-hidden="true" />
								{t().open.copyAll}
							{/if}
						</Button>
					</div>
				{:else if openedKind === 'text'}
					<div class="grid gap-3">
						<Textarea
							class="min-h-64 resize-y font-mono text-sm"
							value={decryptedText}
							readonly
							aria-label={t().open.decryptedSecret}
						/>
						<Button
							type="button"
							variant="outline"
							class="h-11 w-full"
							aria-label={t().open.copySecret}
							onclick={() => {
								void copySecret();
							}}
						>
							{#if copyState === 'copied'}
								<CheckIcon class="size-4" aria-hidden="true" />
								{t().open.copied}
							{:else}
								<CopyIcon class="size-4" aria-hidden="true" />
								{t().open.copy}
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
									{t().open.download}
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
					aria-label={t().open.createOwnLabel}
				>
					{t().open.createOwn}
				</a>
			</section>
		{:else if openStage}
			<LockStageCard
				mode="unlock"
				phase={openStage}
				caption={t().open.stageCaption}
				status={openStage === 'finishing'
					? t().open.stageUnlocked
					: openStage === 'failing'
						? status
						: t().open.stageDecrypting}
			/>
		{:else}
			<section class="grid gap-7">
				<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						{t().open.oneTimeLink}
					</p>
					<h1 class="page-title">{t().open.formTitle}</h1>
				</div>

				<form class="grid gap-5" autocomplete="off" onsubmit={submitOpen}>
					{#if accessModel === 'b'}
						<p class="text-sm text-muted-foreground">
							{t().open.noPassphrase}
						</p>
					{:else if accessModel === 'a'}
						<div class="grid gap-2.5">
							<Label for="open-passphrase" class="micro font-normal text-muted-foreground">
								{t().open.passphraseLabel}
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
									placeholder={hasOpened ? t().open.placeholderOpened : t().open.placeholderRequired}
									bind:value={passphrase}
									disabled={isOpening || hasOpened}
									required
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									class="absolute right-0 top-0 size-11 text-muted-foreground hover:text-foreground"
									aria-label={revealPassphrase ? t().open.hidePassphrase : t().open.showPassphrase}
									title={revealPassphrase ? t().open.hidePassphrase : t().open.showPassphrase}
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
							{t().open.ctaOpened}
						{:else}
							<LockKeyholeOpenIcon class="size-4" aria-hidden="true" />
							{isOpening ? t().open.ctaOpening : t().open.ctaOpen}
						{/if}
					</Button>

					{#if canRetryLoad}
						<Button type="button" variant="outline" class="h-11 w-full" onclick={() => void loadModel()}>
							{t().open.retryLoading}
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
						aria-label={t().open.createInsteadLabel}
					>
						{t().open.createInstead}
					</a>
				{/if}
			</section>
		{/if}
	</div>
</main>
