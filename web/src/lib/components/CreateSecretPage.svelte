<script lang="ts">
import {
	ArrowUpRightIcon,
	ClockIcon,
	EyeIcon,
	EyeOffIcon,
	FileUpIcon,
	FlameIcon,
	LockKeyholeIcon,
	QrCodeIcon,
	ShieldCheckIcon,
	TimerIcon,
	WandSparklesIcon,
	XIcon
} from '@lucide/svelte';
import { onDestroy, onMount } from 'svelte';
import { type ClientLimits, defaultLimits, getConfig } from '#lib/api/config.js';
import { createManagementUrl, isManagementToken } from '#lib/api/management.js';
import {
	type CreateSecretResponse,
	createSecretApiClient,
	createShareUrl,
	DEFAULT_API_BASE_URL,
	type FileUploadProgress,
	type PassphraseEnvelope,
	SecretApiError
} from '#lib/api/secrets.js';
import CredentialForm from '#lib/components/CredentialForm.svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import DeliveryTabs from '#lib/components/DeliveryTabs.svelte';
import LifetimePicker from '#lib/components/LifetimePicker.svelte';
import LockStageCard from '#lib/components/LockStageCard.svelte';
import NativeShareButton from '#lib/components/NativeShareButton.svelte';
import PipeReceivedSecretView, {
	type ReceivedPipePayload
} from '#lib/components/PipeReceivedSecretView.svelte';
import PipeWaitingPanel from '#lib/components/PipeWaitingPanel.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import SecretTypeSelector from '#lib/components/SecretTypeSelector.svelte';
import SuccessCheck from '#lib/components/SuccessCheck.svelte';
import SwitchRow from '#lib/components/SwitchRow.svelte';
import UrlField from '#lib/components/UrlField.svelte';
import { Button } from '#lib/components/ui/button/index.js';
import { Input } from '#lib/components/ui/input/index.js';
import { Label } from '#lib/components/ui/label/index.js';
import { Textarea } from '#lib/components/ui/textarea/index.js';
import { DEFAULT_TTL_SECONDS, MAX_TTL_SECONDS, MIN_TTL_SECONDS } from '#lib/config/ttl.js';
import {
	buildEnvelope,
	CREDENTIAL_TYPES,
	type CredentialEnvelope,
	type CredentialType,
	hasCredentialContent,
	serializeCredential
} from '#lib/credentials/index.js';
import { createAccessVerifier } from '#lib/crypto/access.js';
import { generateSecretIdentity } from '#lib/crypto/age.js';
import { generatePassphrase } from '#lib/crypto/passphrase.js';
import { sealFile, sealText, wrapIdentityOffThread } from '#lib/crypto/secret.js';
import { bundleFiles, needsBundle, type PickedFile } from '#lib/files/bundle.js';
import { readDrop } from '#lib/files/dropped.js';
import { t } from '#lib/i18n/index.js';
import { remainingSecondsFrom } from '#lib/lifetime.js';
import { holdMinimumStage, STAGE_FINISH_MS, sleep } from '#lib/stage-timing.js';
import { clearCreatedDelivery, handOffCreatedDelivery } from '#lib/state/created-delivery.js';
import { createPipeRelay, type PipePayload } from '#lib/state/pipe-relay.svelte.js';
import { cn, formatBytes } from '#lib/utils.js';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';

type StatusKind = 'idle' | 'encrypting' | 'saving' | 'error' | FileUploadProgress['stage'];
type CreateMode = 'text' | 'file' | CredentialType;

let isHydrated = $state(false);

// Defaults until GET /api/config resolves; the server re-enforces both limits.
let limits = $state<ClientLimits>(defaultLimits());
// iOS Safari has no folder picker: render the button only where webkitdirectory exists.
let supportsFolderPick = $state(false);
onMount(() => {
	isHydrated = true;
	supportsFolderPick = 'webkitdirectory' in document.createElement('input');
	void getConfig(DEFAULT_API_BASE_URL).then((resolved) => {
		limits = resolved;
	});
});
const api = $derived(createSecretApiClient({ limits }));

let mode = $state<CreateMode>('text');
let delivery = $state<'link' | 'code'>('link');
let receiveModalOpen = $state(false);
let plaintext = $state('');
let credentialEnvelope = $state<CredentialEnvelope>(buildEnvelope('login'));
let passphrase = $state('');
// Model A: key + access proof from the passphrase; Model B: random key in the URL fragment.
let usePassphrase = $state(false);
let revealPassphrase = $state(false);
let ttlSeconds = $state(DEFAULT_TTL_SECONDS);
// selectedFile is what uploads: the one loose file as-is, or the zipped bundle.
let pickedFiles = $state<PickedFile[]>([]);
let selectedFile = $state<File | null>(null);
let fileInput = $state<HTMLInputElement | null>(null);
let folderInput = $state<HTMLInputElement | null>(null);
let bundling = $state(false);
let dragActive = $state(false);
// Monotonic token: applyFiles runs overlap, so only the latest may commit.
let bundleToken = 0;
let shareUrl = $state('');
let managementUrl = $state('');
let expiresAt = $state('');
let status = $state('');
let statusKind = $state<StatusKind>('idle');
let isCreating = $state(false);
// One controller identifies the current attempt and suppresses stale callbacks.
let abortController = $state<AbortController | null>(null);
let uploadProgress = $state<FileUploadProgress | null>(null);
let stageCardDone = $state(false);

// The link passphrase never reaches a code transfer (encrypted to the receiver's age recipient).
const relay = createPipeRelay({ getPayload: buildPipePayload });
let receivedPipeSecret = $state<ReceivedPipePayload | null>(null);

let qrOpen = $state(false);
let successHeading = $state<HTMLHeadingElement | null>(null);

onDestroy(() => {
	abortController?.abort();
});

$effect(() => {
	if (hasResult) {
		successHeading?.focus();
	}
});

let nowTick = $state(Date.now());
$effect(() => {
	const id = window.setInterval(() => {
		nowTick = Date.now();
	}, 1000);
	return () => window.clearInterval(id);
});

const selectedFileTooLarge = $derived(
	// Raw byte total, so an over-limit batch is rejected before the slow zip.
	pickedFiles.reduce((total, { file }) => total + file.size, 0) > limits.maxFileBytes
);
// Rows group by top-level folder name, so same-named folders merge into one row.
type PickedRow =
	| { kind: 'file'; key: string; picked: PickedFile }
	| { kind: 'folder'; key: string; name: string; count: number; size: number };
const pickedRows = $derived.by(() => {
	const rows: PickedRow[] = [];
	const folders = new Map<string, Extract<PickedRow, { kind: 'folder' }>>();
	for (const picked of pickedFiles) {
		const slash = picked.path.indexOf('/');
		if (slash < 0) {
			rows.push({ kind: 'file', key: fileKey(picked), picked });
			continue;
		}
		const name = picked.path.slice(0, slash);
		const folder = folders.get(name);
		if (folder) {
			folder.count += 1;
			folder.size += picked.file.size;
			continue;
		}
		const row = {
			kind: 'folder' as const,
			key: `folder:${name}`,
			name,
			count: 1,
			size: picked.file.size
		};
		folders.set(name, row);
		rows.push(row);
	}
	return rows;
});
const hasCredentialPayload = $derived(hasCredentialContent(credentialEnvelope));
const hasCreatePayload = $derived(
	mode === 'text'
		? plaintext.trim().length > 0
		: mode === 'file'
			? selectedFile !== null && !selectedFileTooLarge
			: hasCredentialPayload
);
const passphraseReady = $derived(!usePassphrase || passphrase.length > 0);
const canCreate = $derived(
	hasCreatePayload &&
		passphraseReady &&
		Number.isInteger(ttlSeconds) &&
		ttlSeconds >= MIN_TTL_SECONDS &&
		ttlSeconds <= MAX_TTL_SECONDS &&
		!isCreating &&
		!bundling
);
const canPipe = $derived(hasCreatePayload && !isCreating && !bundling && relay.activeCode === null);
const hasResult = $derived(shareUrl.length > 0);
const remainingSeconds = $derived(remainingSecondsFrom(expiresAt, nowTick));

// Disabled CTAs keep pointer events so the cursor reads "blocked" and the title says why.
const BLOCKED_CLASS = 'disabled:pointer-events-auto disabled:cursor-not-allowed';
const blockedTitle = $derived(
	hasCreatePayload
		? delivery === 'link' && usePassphrase && passphrase.length === 0
			? t().create.blocked.passphrase
			: undefined
		: t().create.blocked.empty
);

const stageRingProgress = $derived.by(() => {
	if (uploadProgress === null) {
		return null;
	}
	if (statusKind === 'finalizing') {
		return 1;
	}
	return uploadProgress.total !== undefined && uploadProgress.total > 0
		? (uploadProgress.loaded ?? 0) / uploadProgress.total
		: 0;
});

function submitCreate(event: SubmitEvent): void {
	event.preventDefault();
	// The code tab has no visible submit; block an implicit Enter that would create a link.
	if (delivery === 'code') {
		return;
	}
	void createSecret();
}

async function createSecret(): Promise<void> {
	if (!canCreate) {
		return;
	}

	isCreating = true;
	const stageStartedAt = performance.now();
	const controller = new AbortController();
	abortController = controller;
	uploadProgress = null;
	stageCardDone = false;
	status = t().create.status.encrypting;
	statusKind = 'encrypting';

	try {
		const { created, identity } = await createSelectedSecret();
		if (controller.signal.aborted || abortController !== controller) {
			return;
		}

		plaintext = '';
		passphrase = '';
		revealPassphrase = false;
		clearSelectedFile();
		if (isCredentialMode(mode)) {
			credentialEnvelope = buildEnvelope(mode);
		}
		// shareUrl after the lock beat: hasResult replaces the stage card and would skip "Link ready".
		if (!(await finishStage(stageStartedAt, controller))) {
			return;
		}
		shareUrl = createShareUrl(window.location.origin, created.id, identity);
		expiresAt = created.expires_at;
		status = '';
		statusKind = 'idle';
		await openManagement(created);
	} catch (error) {
		if (error instanceof SecretApiError && error.code === 'upload_cancelled') {
			status = t().create.status.uploadCancelled;
			statusKind = 'idle';
		} else {
			status = error instanceof SecretApiError ? error.message : t().create.status.createFailed;
			statusKind = 'error';
		}
	} finally {
		isCreating = false;
		abortController = null;
		uploadProgress = null;
		stageCardDone = false;
	}
}

// False when the attempt was cancelled or superseded meanwhile.
async function finishStage(startedAt: number, controller: AbortController): Promise<boolean> {
	await holdMinimumStage(startedAt);
	if (controller.signal.aborted || abortController !== controller) {
		return false;
	}
	stageCardDone = true;
	await sleep(STAGE_FINISH_MS);
	return !controller.signal.aborted && abortController === controller;
}

async function openManagement(created: CreateSecretResponse): Promise<void> {
	if (created.management_token && isManagementToken(created.management_token)) {
		managementUrl = createManagementUrl(
			window.location.origin,
			created.id,
			created.management_token
		);
		handOffCreatedDelivery({
			id: created.id,
			token: created.management_token,
			recipientUrl: shareUrl,
			usesPassphrase: usePassphrase
		});
		try {
			await goto(managementUrl);
		} catch {
			clearCreatedDelivery();
			// The delivery already exists; keep its links usable if navigation fails.
		}
	}
}

function cancelCreate(): void {
	abortController?.abort();
}

type CreateResult = {
	created: CreateSecretResponse;
	// Model B only; Model A's identity travels wrapped by the passphrase.
	identity?: string;
};

async function createSelectedSecret(): Promise<CreateResult> {
	const identity = await generateSecretIdentity();
	const modelA = usePassphrase ? await passphraseEnvelope(identity, passphrase) : undefined;
	let created: CreateSecretResponse;
	if (mode === 'file') {
		const sealed = await sealFile(identity, requireSelectedFile());
		status = t().create.status.creatingLink;
		statusKind = 'saving';
		const controller = abortController;
		created = await api.createFileSecret(
			sealed,
			ttlSeconds,
			modelA,
			controller?.signal,
			(progress) => {
				if (controller?.signal.aborted || abortController !== controller) {
					return;
				}
				uploadProgress = progress;
				statusKind = progress.stage;
				status = uploadStatusText(progress);
			}
		);
	} else {
		const text = mode === 'text' ? plaintext : serializeCredential(credentialEnvelope);
		const sealed = await sealText(identity, text);
		status = t().create.status.creatingLink;
		statusKind = 'saving';
		created = await api.createTextSecret(sealed, ttlSeconds, modelA);
	}
	return usePassphrase ? { created } : { created, identity };
}

function uploadStatusText(progress: FileUploadProgress): string {
	const words = t().create.status;
	return progress.stage === 'preparing'
		? words.preparingUpload
		: progress.stage === 'finalizing'
			? words.finalizing
			: progress.total !== undefined && progress.loaded !== undefined
				? words.uploadingProgress(
						formatBytes(progress.loaded),
						formatBytes(progress.total),
						Math.floor((progress.loaded / progress.total) * 100)
					)
				: words.uploading;
}

async function passphraseEnvelope(
	identity: string,
	secretPassphrase: string
): Promise<PassphraseEnvelope> {
	const [access, wrappedIdentity] = await Promise.all([
		createAccessVerifier(secretPassphrase),
		wrapIdentityOffThread(identity, secretPassphrase)
	]);
	return { access, wrappedIdentity };
}

function switchMode(nextMode: CreateMode): void {
	mode = nextMode;
	status = '';
	statusKind = 'idle';
	if (nextMode === 'text') {
		clearSelectedFile();
	} else if (nextMode === 'file') {
		plaintext = '';
	} else {
		plaintext = '';
		clearSelectedFile();
		if (credentialEnvelope.type !== nextMode) {
			credentialEnvelope = buildEnvelope(nextMode);
		}
	}
}

async function applyFiles(files: PickedFile[]): Promise<void> {
	status = '';
	statusKind = 'idle';
	pickedFiles = files;

	// A later run bumps the token, so a stale zip resolving late is dropped.
	const token = ++bundleToken;

	if (files.reduce((total, { file }) => total + file.size, 0) > limits.maxFileBytes) {
		selectedFile = null;
		bundling = false;
		return;
	}
	if (files.length === 0 || !needsBundle(files)) {
		selectedFile = files[0]?.file ?? null;
		bundling = false;
		return;
	}
	bundling = true;
	selectedFile = null;
	try {
		const bundled = await bundleFiles(files);
		if (token === bundleToken) {
			selectedFile = bundled;
		}
	} catch {
		if (token === bundleToken) {
			selectedFile = null;
			status = t().create.status.zipFailed;
			statusKind = 'error';
		}
	} finally {
		if (token === bundleToken) {
			bundling = false;
		}
	}
}

function fileKey({ file, path }: PickedFile): string {
	return `${path}|${file.size}|${file.lastModified}`;
}

function looseFiles(files: FileList | null): PickedFile[] {
	return Array.from(files ?? [], (file) => ({ file, path: file.name }));
}

// Same-identity files are skipped so a double-drop doesn't duplicate.
function addFiles(incoming: PickedFile[]): void {
	const seen = new Set(pickedFiles.map(fileKey));
	const merged = [...pickedFiles];
	for (const file of incoming) {
		if (!seen.has(fileKey(file))) {
			seen.add(fileKey(file));
			merged.push(file);
		}
	}
	void applyFiles(merged);
}

function removeFile(picked: PickedFile): void {
	void applyFiles(pickedFiles.filter((entry) => entry !== picked));
}

function removeFolder(name: string): void {
	void applyFiles(pickedFiles.filter((entry) => !entry.path.startsWith(`${name}/`)));
}

function showEmptyFolder(): void {
	status = t().create.status.folderEmpty;
	statusKind = 'error';
}

function onFileInputChange(event: Event): void {
	const input = event.currentTarget as HTMLInputElement;
	addFiles(looseFiles(input.files));
	// Clear so re-picking the same file still fires onchange for the next add.
	input.value = '';
}

// webkitRelativePath starts with the folder name; empty subfolders are absent.
function onFolderInputChange(event: Event): void {
	const input = event.currentTarget as HTMLInputElement;
	const files = Array.from(input.files ?? [], (file) => ({
		file,
		path: file.webkitRelativePath || file.name
	}));
	input.value = '';
	if (files.length === 0) {
		showEmptyFolder();
		return;
	}
	addFiles(files);
}

// readDrop captures the items synchronously, before the event ends.
async function addDrop(items: DataTransferItemList): Promise<void> {
	let batch: Awaited<ReturnType<typeof readDrop>>;
	try {
		batch = await readDrop(items);
	} catch {
		status = t().create.status.folderReadFailed;
		statusKind = 'error';
		return;
	}
	// A slow folder walk can finish after the form moved on; drop its files.
	if (isCreating || mode !== 'file') {
		return;
	}
	const { files, emptyFolders, skipped } = batch;
	if (files.length > 0) {
		addFiles(files);
	}
	// After addFiles, whose applyFiles run resets the status.
	if (skipped > 0) {
		status = t().create.status.skipped(skipped);
		statusKind = 'error';
	} else if (emptyFolders.length > 0) {
		showEmptyFolder();
	}
}

function hasDroppedFiles(transfer: DataTransfer | null): transfer is DataTransfer {
	return transfer !== null && Array.from(transfer.items).some((item) => item.kind === 'file');
}

function onDragOver(event: DragEvent): void {
	event.preventDefault();
	if (!isCreating) {
		dragActive = true;
	}
}

function onDragLeave(event: DragEvent): void {
	event.preventDefault();
	dragActive = false;
}

function onDrop(event: DragEvent): void {
	event.preventDefault();
	dragActive = false;
	if (isCreating) {
		return;
	}
	if (hasDroppedFiles(event.dataTransfer)) {
		void addDrop(event.dataTransfer.items);
	}
}

// Catch drops outside the zone so the browser doesn't open the file; 'Files' gating keeps text drops.
function canAcceptFileDrop(): boolean {
	return receivedPipeSecret === null && relay.activeCode === null && !hasResult && !isCreating;
}

function onWindowDragOver(event: DragEvent): void {
	if (canAcceptFileDrop() && event.dataTransfer?.types.includes('Files')) {
		// Without a prevented dragover the browser navigates to the dropped file.
		event.preventDefault();
	}
}

function onWindowDrop(event: DragEvent): void {
	// defaultPrevented means the zone's own onDrop already handled this drop.
	if (!canAcceptFileDrop() || event.defaultPrevented) {
		return;
	}
	if (!hasDroppedFiles(event.dataTransfer)) {
		return;
	}
	event.preventDefault();
	const items = event.dataTransfer.items;
	switchMode('file');
	void addDrop(items);
}

function clearSelectedFile(): void {
	// Bump the token so an in-flight zip can't resurrect the cleared bundle.
	bundleToken += 1;
	selectedFile = null;
	pickedFiles = [];
	bundling = false;
	dragActive = false;
	if (fileInput) {
		fileInput.value = '';
	}
	if (folderInput) {
		folderInput.value = '';
	}
}

function requireSelectedFile(): File {
	if (selectedFile === null) {
		throw new Error('file required');
	}
	return selectedFile;
}

function createAnother(): void {
	shareUrl = '';
	managementUrl = '';
	expiresAt = '';
	status = '';
	statusKind = 'idle';
	qrOpen = false;
}

function buildPipePayload(): PipePayload {
	if (mode === 'file' && selectedFile) {
		return { kind: 'file', stream: selectedFile.stream(), filename: selectedFile.name };
	}
	const text = mode === 'text' ? plaintext : serializeCredential(credentialEnvelope);
	return { kind: 'text', stream: streamFromBytes(new TextEncoder().encode(text)) };
}

function streamFromBytes(bytes: Uint8Array): ReadableStream<Uint8Array> {
	return new ReadableStream({
		start(controller) {
			controller.enqueue(bytes);
			controller.close();
		}
	});
}

async function startPipeSend(): Promise<void> {
	if (!canPipe) {
		return;
	}
	await relay.start();
}

function resetPipeAndForm(): void {
	relay.cancel();
	createAnother();
	plaintext = '';
	clearSelectedFile();
}

function formatRemaining(seconds: number): string {
	const safe = Math.max(0, seconds);
	const minutes = Math.floor(safe / 60);
	const remainder = safe % 60;
	return `${String(minutes).padStart(2, '0')}:${String(remainder).padStart(2, '0')}`;
}

function isCredentialMode(value: CreateMode): value is CredentialType {
	const credentialTypes: readonly string[] = CREDENTIAL_TYPES;
	return credentialTypes.includes(value);
}

function typePanelId(type: CreateMode): string {
	return `secret-type-${type}`;
}
</script>

<svelte:head>
	<title>{t().create.headTitle}</title>
</svelte:head>

<svelte:window ondragover={onWindowDragOver} ondrop={onWindowDrop} />

<!-- DeliveryFrame owns the single ReceiveCodeModal (header, code tab sublink, /#code=). -->
<DeliveryFrame
	showEnterCode
	bind:receiveModalOpen={receiveModalOpen}
	onCodeReceived={(secret) => {
		if (relay.activeCode !== null) {
			relay.cancel();
		}
		receivedPipeSecret = secret;
	}}
>
	{#if receivedPipeSecret !== null}
		{#key receivedPipeSecret}
			<PipeReceivedSecretView
				payload={receivedPipeSecret}
				onDone={() => {
					receivedPipeSecret = null;
				}}
			/>
		{/key}
	{:else if relay.activeCode !== null}
		<PipeWaitingPanel
			code={relay.activeCode}
			expiresInSeconds={relay.expiresInSeconds}
			statusText={relay.statusText}
			progressPercent={relay.progressPercent}
			completed={relay.completed}
			failed={relay.failed}
			pending={relay.pending}
			onSend={relay.sendTo}
			onDeny={relay.deny}
			onCancel={relay.cancel}
			onReset={resetPipeAndForm}
		/>
	{:else if hasResult}
		<section class="grid gap-7">
			<div class="grid place-items-center gap-4 text-center">
				<SuccessCheck />
				<div class="grid gap-2">
					<p class="micro flex items-center justify-center gap-1.5 text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						{t().create.result.oneTimeLink}
					</p>
					<h1
						class="page-title"
						tabindex="-1"
						bind:this={successHeading}
					>
						{t().create.result.title}
					</h1>
					<p class="text-sm text-muted-foreground">{t().create.result.opensOnce}</p>
				</div>
				<div class="flex items-center gap-2 font-mono text-sm text-muted-foreground">
					<ClockIcon class="size-4" aria-hidden="true" />
					<span>{t().create.result.expiresIn}</span>
					<span class="tabular-nums text-foreground">{formatRemaining(remainingSeconds)}</span>
				</div>
			</div>

			<div class="grid gap-3">
				<UrlField value={shareUrl} id="share-url" />
				<NativeShareButton recipientUrl={shareUrl} />
				{#if managementUrl}
					<a href={managementUrl} class="text-sm underline underline-offset-4">{t().create.result.openManagement}</a>
				{/if}
				<Button
					type="button"
					variant="outline"
					class="h-11 w-full"
					onclick={() => {
						qrOpen = true;
					}}
				>
					<QrCodeIcon class="size-4" aria-hidden="true" />
					{t().create.result.showQr}
				</Button>
				<aside class="flex gap-3 rounded-lg bg-muted/40 p-4 text-left">
					<LockKeyholeIcon
						class="mt-0.5 size-4 shrink-0 text-muted-foreground"
						aria-hidden="true"
					/>
					<div class="grid gap-1">
						{#if usePassphrase}
							<strong class="text-sm font-medium">{t().create.result.passphraseTitle}</strong>
							<p class="text-sm text-muted-foreground">
								{t().create.result.passphraseBody}
							</p>
						{:else}
							<strong class="text-sm font-medium">{t().create.result.linkOnlyTitle}</strong>
							<p class="text-sm text-muted-foreground">
								{t().create.result.linkOnlyBody}
							</p>
						{/if}
					</div>
				</aside>
				<Button type="button" variant="ghost" class="h-9 w-full text-sm" onclick={createAnother}>
					{t().create.result.createAnother}
				</Button>
			</div>
		</section>
	{:else}
		<section class="grid gap-7">
			<div class="grid gap-1.5">
				<p class="micro flex items-center gap-1.5 text-muted-foreground">
					<ShieldCheckIcon class="size-3.5 text-primary" aria-hidden="true" />
					{t().create.tagline}
				</p>
				<h1 class="page-title">{delivery === 'code' ? t().create.titleCode : t().create.titleLink}</h1>
				{#if delivery === 'code'}
					<p class="text-sm text-muted-foreground">
						{t().create.receivedCode}
						<button
							type="button"
							class="font-medium text-primary underline underline-offset-4 hover:text-primary/80"
							onclick={() => {
								receiveModalOpen = true;
							}}
						>{t().create.enterItHere}</button>
					</p>
				{:else}
					<p class="text-sm text-muted-foreground">
						{t().create.needOne}
						<a
							class="font-medium text-primary underline underline-offset-4 hover:text-primary/80"
							href={resolve('request')}
						>{t().create.requestSecret}</a>
					</p>
				{/if}
			</div>

			<form
				class="grid gap-6"
				autocomplete="off"
				onsubmit={submitCreate}
			>
				<DeliveryTabs bind:delivery={delivery} disabled={!isHydrated || isCreating} />

				<SecretTypeSelector
					value={mode}
					disabled={!isHydrated || isCreating}
					panelId={typePanelId}
					onSelect={switchMode}
				/>

				{#if isCreating}
					<!-- Cancel only stops the encrypted PUT; finalize has its own status. -->
					<LockStageCard
						mode="lock"
						phase={stageCardDone ? 'finishing' : 'waiting'}
						caption={t().create.stage.caption}
						status={stageCardDone ? t().create.stage.ready : status}
						progress={stageRingProgress}
						cancellable={statusKind === 'uploading'}
						onCancel={cancelCreate}
					/>
				{:else}

				{#if mode === 'text'}
					<div
						class="grid gap-2.5"
						id={typePanelId(mode)}
						role="tabpanel"
						aria-labelledby={`${typePanelId(mode)}-tab`}
					>
						<Label for="secret-text" class="text-sm font-medium">{t().create.text.label}</Label>
						<Textarea
							id="secret-text"
							class="min-h-44 resize-y px-3.5 py-3"
							placeholder={t().create.text.placeholder}
							bind:value={plaintext}
							disabled={isCreating}
							required
						/>
					</div>
				{:else if mode === 'file'}
					<div
						class="grid gap-2.5"
						id={typePanelId(mode)}
						role="tabpanel"
						aria-labelledby={`${typePanelId(mode)}-tab`}
					>
						<span class="text-sm font-medium">{t().create.files.label}</span>
						<!-- The folder button sits outside the label so its click opens only the folder picker. -->
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div
							class={cn(
								'grid rounded-lg border border-dashed transition-colors has-[#secret-file:focus-visible]:border-primary has-[#secret-file:focus-visible]:ring-2 has-[#secret-file:focus-visible]:ring-ring',
								dragActive
									? 'border-primary bg-primary/5'
									: 'border-border bg-muted/20 hover:bg-muted/40',
								isCreating && 'pointer-events-none opacity-60'
							)}
							ondragover={onDragOver}
							ondragenter={onDragOver}
							ondragleave={onDragLeave}
							ondrop={onDrop}
						>
							<label
								class={cn(
									'grid cursor-pointer place-items-center gap-2 px-4 text-center',
									supportsFolderPick ? 'pt-8 pb-1' : 'py-8'
								)}
							>
								<FileUpIcon
									class={cn('size-6', dragActive ? 'text-primary' : 'text-muted-foreground')}
									aria-hidden="true"
								/>
								<p class="text-sm">
									<span class="font-medium text-foreground">{t().create.files.dropLead}</span>
										<span class="text-muted-foreground">{t().create.files.dropRest}</span>
								</p>
								<p class="micro text-muted-foreground">
									<!-- micro uppercases its text; byte units are case-significant (MiB, KiB). -->
									{t().create.files.limitLead}
									<span class="normal-case">{formatBytes(limits.maxFileBytes)}</span>
								</p>
								<input
									bind:this={fileInput}
									id="secret-file"
									type="file"
									multiple
									class="sr-only"
									aria-label={t().create.files.addFiles}
									disabled={isCreating}
									onchange={onFileInputChange}
								/>
							</label>
							{#if supportsFolderPick}
								<Button
									variant="link"
									class="mx-auto mb-3 min-h-11 text-muted-foreground hover:text-foreground"
									disabled={isCreating}
									onclick={() => folderInput?.click()}
								>
									{t().create.files.chooseFolder}
								</Button>
								<input
									bind:this={folderInput}
									id="secret-folder"
									type="file"
									webkitdirectory
									hidden
									disabled={isCreating}
									onchange={onFolderInputChange}
								/>
							{/if}
						</div>
						{#if pickedFiles.length > 0}
							<ul class="grid gap-1.5">
								{#each pickedRows as row (row.key)}
									<li
										class="flex min-h-11 min-w-0 items-center justify-between gap-2 rounded-md border border-border bg-muted/30 px-3 text-sm"
									>
										{#if row.kind === 'folder'}
											<span class="truncate text-muted-foreground" title={`${row.name}/`}>{row.name}/</span>
											<span class="flex shrink-0 items-center gap-2">
												<span class="font-mono text-xs text-muted-foreground">
													{t().create.files.count(row.count)} · {formatBytes(row.size)}
												</span>
												<button
													type="button"
													class="-mr-3 grid size-11 shrink-0 place-items-center rounded-r-md text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
													aria-label={t().create.files.removeFolder(row.name)}
													disabled={isCreating}
													onclick={() => {
														removeFolder(row.name);
													}}
												>
													<XIcon class="size-4" aria-hidden="true" />
												</button>
											</span>
										{:else}
											<span class="truncate text-muted-foreground" title={row.picked.path}>{row.picked.path}</span>
											<span class="flex shrink-0 items-center gap-2">
												<span class="font-mono text-xs text-muted-foreground">
													{formatBytes(row.picked.file.size)}
												</span>
												<button
													type="button"
													class="-mr-3 grid size-11 shrink-0 place-items-center rounded-r-md text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
													aria-label={t().create.files.removeFile(row.picked.path)}
													disabled={isCreating}
													onclick={() => {
														removeFile(row.picked);
													}}
												>
													<XIcon class="size-4" aria-hidden="true" />
												</button>
											</span>
										{/if}
									</li>
								{/each}
							</ul>
							{#if bundling}
								<p
									class="micro text-right text-muted-foreground"
									role="status"
									aria-live="polite"
									>
										{t().create.files.zipping(pickedFiles.length)}
									</p>
							{:else if selectedFile && selectedFile !== pickedFiles[0].file}
									<p class="micro text-right normal-case text-muted-foreground [overflow-wrap:anywhere]">
										{t().create.files.count(pickedFiles.length)} → {selectedFile.name} · {formatBytes(selectedFile.size)}
									</p>
							{/if}
						{/if}
						{#if selectedFileTooLarge}
								<p class="text-sm text-destructive" role="alert" aria-live="assertive">
									{t().create.files.tooLarge(formatBytes(limits.maxFileBytes))}
								</p>
						{/if}
					</div>
				{:else}
					<div
						class="grid gap-2.5"
						id={typePanelId(mode)}
						role="tabpanel"
						aria-labelledby={`${typePanelId(mode)}-tab`}
					>
						<span class="text-sm font-medium">{t().create.types[mode]}</span>
							<CredentialForm bind:envelope={credentialEnvelope} disabled={isCreating} />
					</div>
				{/if}

				{#snippet ctaFooter({
					type,
					label,
					disabled,
					onclick,
					icon: Icon,
					caption
				}: {
					type: 'submit' | 'button';
					label: string;
					disabled: boolean;
					onclick?: () => void;
					icon: typeof FlameIcon;
					caption: string;
				})}
					<div class="grid gap-3">
						<Button
							{type}
							variant="toggleActive"
							size="cta"
							{disabled}
							class={BLOCKED_CLASS}
							title={disabled ? blockedTitle : undefined}
							{onclick}
						>
							{isCreating ? t().create.cta.creating : label}
						</Button>
						<p class="micro flex items-center justify-center gap-1.5 text-muted-foreground">
							<Icon class="size-3" aria-hidden="true" />
							<span>{caption}</span>
						</p>
					</div>
				{/snippet}

				{#if delivery === 'link'}
					<SwitchRow
						id="use-passphrase"
						bind:checked={usePassphrase}
						label={t().create.passphrase.toggle}
						icon={LockKeyholeIcon}
						infoLabel={t().create.passphrase.infoLabel}
						describedBy="passphrase-privacy"
						disabled={isCreating}
					>
						{#snippet details()}
							<p class="mb-1 text-xs font-medium text-foreground">{t().create.passphrase.whyTitle}</p>
							<p class="text-[0.8125rem] leading-snug text-muted-foreground">{t().create.passphrase.whyLocked}</p>
							<p class="text-[0.8125rem] leading-snug text-muted-foreground">{t().create.passphrase.whyNeverSent}</p>
						{/snippet}
						{#snippet field()}
							<div class="relative">
								<Input
									id="secret-passphrase"
									name="flick-passphrase"
									type={revealPassphrase ? 'text' : 'password'}
									placeholder={t().create.passphrase.placeholder}
									aria-label={t().create.passphrase.fieldLabel}
									autocomplete="new-password"
									autocapitalize="none"
									spellcheck="false"
									data-1p-ignore="true"
									data-bwignore="true"
									data-lpignore="true"
									enterkeyhint="done"
									class="h-11 pr-22 font-mono"
									bind:value={passphrase}
									disabled={isCreating}
									required
								/>
								<div class="absolute right-0 top-0 flex h-11 items-center">
									<Button
										type="button"
										variant="ghost"
										size="icon"
										class="size-11 text-muted-foreground hover:text-foreground"
										aria-label={t().create.passphrase.generate}
										title={t().create.passphrase.generate}
										disabled={isCreating}
										onclick={() => {
											passphrase = generatePassphrase();
											revealPassphrase = true;
										}}
									>
										<WandSparklesIcon class="size-4" aria-hidden="true" />
									</Button>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										class="size-11 text-muted-foreground hover:text-foreground"
										aria-label={revealPassphrase ? t().create.passphrase.hide : t().create.passphrase.show}
										title={revealPassphrase ? t().create.passphrase.hide : t().create.passphrase.show}
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
						{/snippet}
					</SwitchRow>

					<div class="grid gap-2.5">
						<span class="flex items-center gap-1.5 text-sm font-medium">
							<ClockIcon class="size-4" aria-hidden="true" />
							{t().create.expiresAfter}
						</span>
						<LifetimePicker bind:seconds={ttlSeconds} disabled={isCreating} groupLabel={t().create.lifetimeGroup} label="" />
					</div>

					{@render ctaFooter({
						type: 'submit',
						label: t().create.cta.createLink,
						disabled: !canCreate,
						icon: FlameIcon,
						caption: t().create.linkCaption
					})}
				{:else}
					{@render ctaFooter({
						type: 'button',
						label: t().create.cta.startLive,
						disabled: !canPipe,
						onclick: startPipeSend,
						icon: TimerIcon,
						caption: t().create.liveCaption
					})}
				{/if}
			{/if}

			{#if status.length > 0 && !isCreating}
				<p
					class="text-sm"
					class:text-muted-foreground={statusKind !== 'error'}
					class:text-destructive={statusKind === 'error'}
					role={statusKind === 'error' ? 'alert' : 'status'}
					aria-live={statusKind === 'error' ? 'assertive' : 'polite'}
				>
					{status}
				</p>
				{/if}
			</form>
		</section>
	{/if}

	{#if receivedPipeSecret === null}
		<!-- New tab so the in-progress form survives. -->
		<nav
			aria-label={t().create.guideNav}
			class="flex flex-wrap justify-center gap-x-5 gap-y-2 border-t pt-3.5 text-[0.8125rem] text-muted-foreground"
		>
			<a
				target="_blank"
				rel="noopener noreferrer"
				href={resolve('guides/')}
				class="inline-flex items-center gap-1 py-2 underline-offset-4 hover:underline"
			>{t().create.howItWorks}
				<ArrowUpRightIcon class="size-3" aria-hidden="true" />
			</a>
		</nav>
	{/if}
</DeliveryFrame>

<QrModal bind:open={qrOpen} url={shareUrl} />
