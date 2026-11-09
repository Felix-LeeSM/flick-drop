<script lang="ts">
import {
	ArrowUpRightIcon,
	ClockIcon,
	EyeIcon,
	EyeOffIcon,
	FileUpIcon,
	FlameIcon,
	LinkIcon,
	LockKeyholeIcon,
	QrCodeIcon,
	ShieldCheckIcon,
	UploadIcon,
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
	SecretApiError,
	type TtlSeconds
} from '#lib/api/secrets.js';
import CreateStageCard from '#lib/components/CreateStageCard.svelte';
import CredentialForm from '#lib/components/CredentialForm.svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import DeliveryTabs from '#lib/components/DeliveryTabs.svelte';
import LifetimePicker from '#lib/components/LifetimePicker.svelte';
import NativeShareButton from '#lib/components/NativeShareButton.svelte';
import PipeWaitingPanel from '#lib/components/PipeWaitingPanel.svelte';
import QrModal from '#lib/components/QrModal.svelte';
import ReceiveCodeModal from '#lib/components/ReceiveCodeModal.svelte';
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
	CREDENTIAL_TEMPLATES,
	CREDENTIAL_TYPES,
	type CredentialEnvelope,
	type CredentialType,
	hasCredentialContent,
	serializeCredential
} from '#lib/credentials/index.js';
import { createAccessVerifier } from '#lib/crypto/access.js';
import { generateSecretIdentity } from '#lib/crypto/age.js';
import { generatePassphrase } from '#lib/crypto/passphrase.js';
import {
	computePipePassphrase,
	deriveConfirmationCode,
	encryptPipeStream,
	initPipeRendezvous,
	sendPipeStream,
	streamToBytes,
	waitForReceiver
} from '#lib/crypto/pipe.js';
import { type SealedFile, sealFile, sealText, wrapIdentityOffThread } from '#lib/crypto/secret.js';
import { bundleFiles } from '#lib/files/bundle.js';
import { remainingSecondsFrom } from '#lib/lifetime.js';
import { clearCreatedDelivery, handOffCreatedDelivery } from '#lib/state/created-delivery.js';
import { cn, formatBytes } from '#lib/utils.js';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';

type StatusKind = 'idle' | 'encrypting' | 'saving' | 'error' | FileUploadProgress['stage'];
type CreateMode = 'text' | 'file' | CredentialType;

let isHydrated = $state(false);

// File size limits come from the server at boot (GET /api/config). They start at
// the built-in defaults so the create flow is usable immediately, then settle
// once the fetch resolves. The server re-enforces both limits, so a stale
// default cannot let an oversized file through.
let limits = $state<ClientLimits>(defaultLimits());
onMount(() => {
	isHydrated = true;
	void getConfig(DEFAULT_API_BASE_URL).then((resolved) => {
		limits = resolved;
	});
});
const api = $derived(createSecretApiClient({ limits }));

let mode = $state<CreateMode>('text');
// The delivery choice is the first control of the page; the heading and the
// option/submit blocks below adapt to it.
let delivery = $state<'link' | 'code'>('link');
let receiveModalOpen = $state(false);
let plaintext = $state('');
let credentialEnvelope = $state<CredentialEnvelope>(buildEnvelope('login'));
let passphrase = $state('');
// Model A (true) derives key + access proof from a passphrase; Model B (false)
// generates a random key carried in the URL fragment. See security-model.md.
// Both toggles default OFF: the link key / 6-digit code already encrypts, and
// the confirmation step is opt-in.
let usePassphrase = $state(false);
let revealPassphrase = $state(false);
let ttlSeconds = $state(DEFAULT_TTL_SECONDS);
// pickedFiles is what the user chose; selectedFile is what actually uploads —
// the same File when one was picked, or the zipped bundle when several were.
let pickedFiles = $state<File[]>([]);
let selectedFile = $state<File | null>(null);
let fileInput = $state<HTMLInputElement | null>(null);
let bundling = $state(false);
let dragActive = $state(false);
// Monotonic token: bundling is async and runs can overlap (add, then remove
// mid-zip), so only the latest applyFiles run may commit its result.
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
// The stage card's done beat: every step checked, shown briefly before the
// manage-page handoff takes over.
let stageCardDone = $state(false);

// Live Pipe Relay (Send with code)
let pipeActiveCode = $state<string | null>(null);
let pipeSalt = $state('');
let pipeStatus = $state('Waiting for receiver to enter code...');
let pipeProgress = $state(0);
let pipeCompleted = $state(false);
let pipeRequireApproval = $state(false);
let pipeApprovalStatus = $state<'idle' | 'pending' | 'approved' | 'rejected'>('idle');
let pipeMatchCode = $state('482');
let pipeExpiresIn = $state(180);
let pipeAbortController = $state<AbortController | null>(null);

onDestroy(() => {
	abortController?.abort();
	pipeAbortController?.abort();
});
let qrOpen = $state(false);
let successHeading = $state<HTMLHeadingElement | null>(null);

// Move focus to the success heading when the result panel replaces the form.
$effect(() => {
	if (hasResult) {
		successHeading?.focus();
	}
});

// 1s tick drives the expiry countdown in the success panel.
let nowTick = $state(Date.now());
$effect(() => {
	const id = window.setInterval(() => {
		nowTick = Date.now();
	}, 1000);
	return () => window.clearInterval(id);
});

const selectedFileTooLarge = $derived(
	// Judge on the raw byte total, not the zip size: an over-limit batch is
	// rejected before the slow zip runs. The server re-enforces the exact size.
	pickedFiles.reduce((total, file) => total + file.size, 0) > limits.maxFileBytes
);
const hasCredentialPayload = $derived(hasCredentialContent(credentialEnvelope));
const hasCreatePayload = $derived(
	mode === 'text'
		? plaintext.trim().length > 0
		: mode === 'file'
			? selectedFile !== null && !selectedFileTooLarge
			: hasCredentialPayload
);
const canCreate = $derived(
	hasCreatePayload &&
		(!usePassphrase || passphrase.length > 0) &&
		Number.isInteger(ttlSeconds) &&
		ttlSeconds >= MIN_TTL_SECONDS &&
		ttlSeconds <= MAX_TTL_SECONDS &&
		!isCreating &&
		!bundling
);
const canPipe = $derived(
	hasCreatePayload &&
		(!usePassphrase || passphrase.length > 0) &&
		!isCreating &&
		!bundling &&
		pipeActiveCode === null
);
const hasResult = $derived(shareUrl.length > 0);
const remainingSeconds = $derived(remainingSecondsFrom(expiresAt, nowTick));

// Round 22 pattern: a disabled action keeps pointer events so the cursor reads
// "blocked", and the title tooltip states why. Both CTAs share one look, so
// they share the blocked affordance too.
const BLOCKED_CLASS = 'disabled:pointer-events-auto disabled:cursor-not-allowed';
const blockedTitle = $derived(
	hasCreatePayload
		? usePassphrase && passphrase.length === 0
			? 'Set a passphrase to finish protecting this secret'
			: undefined
		: 'Add a message, file or credential first'
);

// P2 stage card: the create folds the form into a step card while running.
// File creates upload, so they get three steps; text and credentials go
// straight from local encryption to the link.
const FILE_STAGE_STEPS = [
	{ icon: LockKeyholeIcon, label: 'Encrypt on this device' },
	{ icon: UploadIcon, label: 'Upload encrypted file' },
	{ icon: LinkIcon, label: 'Create the one-time link' }
];
const INLINE_STAGE_STEPS = [
	{ icon: LockKeyholeIcon, label: 'Encrypt on this device' },
	{ icon: LinkIcon, label: 'Create the one-time link' }
];
const stageSteps = $derived(mode === 'file' ? FILE_STAGE_STEPS : INLINE_STAGE_STEPS);
const stageStep = $derived.by(() => {
	if (stageCardDone) {
		return stageSteps.length;
	}
	if (statusKind === 'encrypting') {
		return 0;
	}
	if (mode === 'file') {
		return statusKind === 'finalizing' ? 2 : 1;
	}
	return statusKind === 'saving' ? 1 : 0;
});
const stageCardProgress = $derived(
	statusKind === 'uploading' && uploadProgress?.total !== undefined && uploadProgress.total > 0
		? (uploadProgress.loaded ?? 0) / uploadProgress.total
		: null
);

function submitCreate(event: SubmitEvent): void {
	event.preventDefault();
	// On the code tab the form has no visible submit button; an implicit Enter
	// submit there would silently create a link the user is not asking for.
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
	const controller = new AbortController();
	abortController = controller;
	uploadProgress = null;
	stageCardDone = false;
	status = 'Encrypting in your browser…';
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
		// Every stage step is checked; let the done beat land before the
		// manage-page handoff takes over. shareUrl must stay after the beat:
		// it flips hasResult, and hasResult replaces the stage card, so an
		// earlier assignment would skip "Link ready" entirely.
		stageCardDone = true;
		await new Promise((beatTimer) => setTimeout(beatTimer, 900));
		if (controller.signal.aborted || abortController !== controller) {
			return;
		}
		shareUrl = createShareUrl(window.location.origin, created.id, identity);
		expiresAt = created.expires_at;
		status = '';
		statusKind = 'idle';
		await openManagement(created);
	} catch (error) {
		// A user-cancelled upload returns to the idle form, not a red error — the
		// user chose to stop, nothing failed.
		if (error instanceof SecretApiError && error.code === 'upload_cancelled') {
			status = 'Upload cancelled.';
			statusKind = 'idle';
		} else {
			status =
				error instanceof SecretApiError ? error.message : 'Could not create link. Try again.';
			statusKind = 'error';
		}
	} finally {
		isCreating = false;
		abortController = null;
		uploadProgress = null;
		stageCardDone = false;
	}
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
	// The age identity for Model B's URL fragment; omitted for Model A, whose
	// identity travels wrapped by the passphrase.
	identity?: string;
};

// One fresh age identity per secret (contracts/age-format-v1.md). Model A
// sends it wrapped with the passphrase plus a separate access proof; Model B
// puts it in the link.
async function createSelectedSecret(): Promise<CreateResult> {
	const identity = await generateSecretIdentity();
	const modelA = usePassphrase ? await passphraseEnvelope(identity, passphrase) : undefined;
	let created: CreateSecretResponse;
	if (mode === 'file') {
		created = await createFile(await sealFile(identity, requireSelectedFile()), modelA);
	} else {
		const text = mode === 'text' ? plaintext : serializeCredential(credentialEnvelope);
		created = await createText(await sealText(identity, text), ttlSeconds, modelA);
	}
	return usePassphrase ? { created } : { created, identity };
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

function createText(
	body: Uint8Array,
	ttl: TtlSeconds,
	modelA?: PassphraseEnvelope
): Promise<CreateSecretResponse> {
	status = 'Creating link…';
	statusKind = 'saving';
	return api.createTextSecret(body, ttl, modelA);
}

function createFile(
	sealed: SealedFile,
	modelA?: PassphraseEnvelope
): Promise<CreateSecretResponse> {
	status = 'Creating link…';
	statusKind = 'saving';
	const controller = abortController;
	return api.createFileSecret(sealed, ttlSeconds, modelA, controller?.signal, (progress) => {
		if (controller?.signal.aborted || abortController !== controller) {
			return;
		}
		uploadProgress = progress;
		statusKind = progress.stage;
		status =
			progress.stage === 'preparing'
				? 'Preparing encrypted upload…'
				: progress.stage === 'finalizing'
					? 'Finalizing link…'
					: progress.total !== undefined && progress.loaded !== undefined
						? `Uploading encrypted file · ${formatBytes(progress.loaded)} of ${formatBytes(progress.total)} (${Math.floor((progress.loaded / progress.total) * 100)}%)`
						: 'Uploading encrypted file…';
	});
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

// Recomputes the single File that uploads from the current batch. One file rides
// as-is; several are zipped into bundle.zip (async, so the form shows "Zipping").
async function applyFiles(files: File[]): Promise<void> {
	status = '';
	statusKind = 'idle';
	pickedFiles = files;

	// Claim this run. A later run (or a sync return below) bumps the token, so a
	// stale zip resolving late is dropped instead of resurrecting a removed file
	// or clearing `bundling` while a newer run is still going.
	const token = ++bundleToken;

	// Pre-flight on the raw total before the slow zip: skip building an unusable
	// bundle when the batch already exceeds the limit (selectedFileTooLarge shows
	// it). The server re-enforces the exact bundle size.
	if (files.reduce((total, file) => total + file.size, 0) > limits.maxFileBytes) {
		selectedFile = null;
		bundling = false;
		return;
	}
	if (files.length <= 1) {
		selectedFile = files[0] ?? null;
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
			status = 'Could not zip those files. Try again.';
			statusKind = 'error';
		}
	} finally {
		if (token === bundleToken) {
			bundling = false;
		}
	}
}

function fileKey(file: File): string {
	return `${file.name}|${file.size}|${file.lastModified}`;
}

// Accumulate: each pick/drop adds to the batch. Same-identity files are skipped so
// a double-drop doesn't duplicate; order is preserved.
function addFiles(incoming: File[]): void {
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

function removeFile(index: number): void {
	void applyFiles(pickedFiles.filter((_, position) => position !== index));
}

function onFileInputChange(event: Event): void {
	const input = event.currentTarget as HTMLInputElement;
	addFiles(Array.from(input.files ?? []));
	// Clear so re-picking the same file still fires onchange for the next add.
	input.value = '';
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
	const files = Array.from(event.dataTransfer?.files ?? []);
	if (files.length > 0) {
		addFiles(files);
	}
}

// The drop zone above only spans the file panel, so a drop landing elsewhere on
// the screen would open the file in the browser instead. These window handlers
// accept that drop, switching to the File tab first when a different tab is
// open — the same behavior as a manual tab click. Gating on 'Files' keeps
// native text drops into the textarea working.
function canAcceptFileDrop(): boolean {
	return pipeActiveCode === null && !hasResult && !isCreating;
}

function onWindowDragOver(event: DragEvent): void {
	if (canAcceptFileDrop() && event.dataTransfer?.types.includes('Files')) {
		// Without a prevented dragover the drop event never fires and the
		// browser navigates to the dragged file.
		event.preventDefault();
	}
}

function onWindowDrop(event: DragEvent): void {
	// defaultPrevented means the zone's own onDrop already handled this drop.
	if (!canAcceptFileDrop() || event.defaultPrevented) {
		return;
	}
	const files = Array.from(event.dataTransfer?.files ?? []);
	if (files.length === 0) {
		return;
	}
	event.preventDefault();
	switchMode('file');
	addFiles(files);
}

function clearSelectedFile(): void {
	// Bump the token so an in-flight zip (e.g. bundling still running when the
	// user switches mode) can't resolve late and resurrect the cleared bundle.
	bundleToken += 1;
	selectedFile = null;
	pickedFiles = [];
	bundling = false;
	dragActive = false;
	if (fileInput) {
		fileInput.value = '';
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

async function startPipeSend(): Promise<void> {
	if (!canPipe) {
		return;
	}
	pipeAbortController?.abort();
	pipeAbortController = new AbortController();
	const signal = pipeAbortController.signal;

	try {
		pipeStatus = 'Initializing live transfer session...';
		const session = await initPipeRendezvous(DEFAULT_API_BASE_URL, fetch);
		pipeActiveCode = session.code;
		pipeSalt = session.salt;
		pipeMatchCode = deriveConfirmationCode(session.code);
		pipeExpiresIn = session.expiresIn;
		pipeApprovalStatus = 'idle';
		pipeCompleted = false;
		pipeProgress = 0;
		pipeStatus = pipeRequireApproval
			? 'Waiting for receiver to enter code... (confirmation required)'
			: 'Waiting for receiver to enter code...';

		await waitForReceiver(DEFAULT_API_BASE_URL, session.code, fetch, signal);

		if (pipeRequireApproval) {
			pipeApprovalStatus = 'pending';
			pipeStatus = 'Receiver connected. Please confirm transfer.';
		} else {
			await executePipeStream();
		}
	} catch (err) {
		if (signal.aborted) {
			return;
		}
		pipeStatus = err instanceof Error ? err.message : 'Live transfer failed';
		pipeApprovalStatus = 'rejected';
	}
}

async function handlePipeApprove(): Promise<void> {
	pipeApprovalStatus = 'approved';
	pipeStatus = 'Approved. Encrypting and streaming chunks...';
	await executePipeStream();
}

function handlePipeReject(): void {
	pipeApprovalStatus = 'rejected';
	pipeStatus = 'Transfer denied. Session wiped from memory.';
	pipeAbortController?.abort();
}

async function executePipeStream(): Promise<void> {
	if (!pipeActiveCode) {
		return;
	}
	const signal = pipeAbortController?.signal;

	try {
		const pipePassphrase = computePipePassphrase(
			pipeActiveCode,
			pipeSalt,
			usePassphrase ? passphrase : ''
		);

		let rawStream: ReadableStream<Uint8Array>;
		let kind: 'text' | 'file' = 'text';
		let filename: string | undefined;

		if (mode === 'file' && selectedFile) {
			kind = 'file';
			filename = selectedFile.name;
			rawStream = selectedFile.stream();
		} else if (mode === 'text') {
			kind = 'text';
			const encoded = new TextEncoder().encode(plaintext);
			rawStream = new ReadableStream({
				start(controller) {
					controller.enqueue(encoded);
					controller.close();
				}
			});
		} else {
			kind = 'text';
			const serialized = serializeCredential(credentialEnvelope);
			const encoded = new TextEncoder().encode(serialized);
			rawStream = new ReadableStream({
				start(controller) {
					controller.enqueue(encoded);
					controller.close();
				}
			});
		}

		pipeStatus = 'Encrypting stream on-the-fly (age format)...';
		pipeProgress = 25;
		const encryptedStream = await encryptPipeStream(rawStream, pipePassphrase);

		pipeStatus = 'Streaming encrypted bytes directly to receiver...';
		pipeProgress = 50;

		let encFilenameHeader: string | undefined;
		if (filename) {
			const filenameBytes = new TextEncoder().encode(filename);
			const encryptedFilenameStream = await encryptPipeStream(filenameBytes, pipePassphrase);
			const encBytes = await streamToBytes(encryptedFilenameStream);
			encFilenameHeader = btoa(String.fromCharCode(...encBytes));
		}

		await sendPipeStream({
			baseUrl: DEFAULT_API_BASE_URL,
			code: pipeActiveCode,
			stream: encryptedStream,
			kind,
			encryptedFilename: encFilenameHeader,
			fetcher: fetch,
			signal
		});

		pipeProgress = 100;
		pipeCompleted = true;
		pipeStatus = 'Transfer complete!';
	} catch (err) {
		if (signal?.aborted) {
			return;
		}
		pipeStatus = err instanceof Error ? err.message : 'Streaming failed';
		pipeApprovalStatus = 'rejected';
	}
}

function cancelPipe(): void {
	pipeAbortController?.abort();
	pipeActiveCode = null;
	pipeStatus = '';
	pipeProgress = 0;
	pipeCompleted = false;
	pipeApprovalStatus = 'idle';
}

function resetPipeAndForm(): void {
	cancelPipe();
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

function modeLabel(value: CreateMode): string {
	if (value === 'text') {
		return 'Text';
	}
	if (value === 'file') {
		return 'File';
	}
	return CREDENTIAL_TEMPLATES.find((template) => template.type === value)?.label ?? value;
}

// Shared with the type tablist: each tab's aria-controls and its panel's
// role="tabpanel" aria-labelledby both derive from this id.
function typePanelId(type: CreateMode): string {
	return `secret-type-${type}`;
}
</script>

<svelte:head>
	<title>Create secret - Flick</title>
</svelte:head>

<svelte:window ondragover={onWindowDragOver} ondrop={onWindowDrop} />

<!-- The header keeps logo + theme only: the request and code entry points live in
     the adaptive heading below (prototype round R1), so `link`/`showEnterCode`
     stay unpassed. SiteHeader still owns the #code= deep-link modal. -->
<DeliveryFrame>
	{#if pipeActiveCode !== null}
		<section class="grid gap-6 rounded-2xl border border-border bg-card p-6 shadow-sm">
			<PipeWaitingPanel
				code={pipeActiveCode}
				expiresInSeconds={pipeExpiresIn}
				statusText={pipeStatus}
				progressPercent={pipeProgress}
				completed={pipeCompleted}
				requireApproval={pipeRequireApproval}
				matchCode={pipeMatchCode}
				approvalStatus={pipeApprovalStatus}
				onApprove={handlePipeApprove}
				onReject={handlePipeReject}
				onCancel={cancelPipe}
				onReset={resetPipeAndForm}
			/>
		</section>
	{:else if hasResult}
		<section class="grid gap-7">
			<div class="grid place-items-center gap-4 text-center">
				<SuccessCheck />
				<div class="grid gap-2">
						<p class="micro flex items-center justify-center gap-1.5 text-burn">
						<FlameIcon class="size-3.5" aria-hidden="true" />
						one-time link
					</p>
					<h1
						class="page-title"
						tabindex="-1"
						bind:this={successHeading}
						>
							Link created
						</h1>
					<p class="text-sm text-muted-foreground">Opens once, then burns. Forward it fast.</p>
				</div>
					<div class="flex items-center gap-2 font-mono text-sm text-muted-foreground">
					<ClockIcon class="size-4" aria-hidden="true" />
					<span>EXPIRES IN</span>
					<span class="tabular-nums text-foreground">{formatRemaining(remainingSeconds)}</span>
				</div>
			</div>

			<div class="grid gap-3">
				<UrlField value={shareUrl} id="share-url" />
				<NativeShareButton recipientUrl={shareUrl} />
				{#if managementUrl}
						<a href={managementUrl} class="text-sm underline underline-offset-4">Open private management page</a>
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
					Show QR
				</Button>
					<aside class="flex gap-3 rounded-lg bg-muted/40 p-4 text-left">
					<LockKeyholeIcon
						class="mt-0.5 size-4 shrink-0 text-muted-foreground"
						aria-hidden="true"
					/>
					<div class="grid gap-1">
						{#if usePassphrase}
							<strong class="text-sm font-medium">Send the passphrase separately.</strong>
								<p class="text-sm text-muted-foreground">
									The recipient needs both the link and the passphrase to open this.
								</p>
						{:else}
							<strong class="text-sm font-medium">Share only with your recipient.</strong>
								<p class="text-sm text-muted-foreground">
									Anyone with this full link can open it once.
								</p>
						{/if}
					</div>
				</aside>
					<Button type="button" variant="ghost" class="h-9 w-full text-sm" onclick={createAnother}>
						Create another
					</Button>
			</div>
		</section>
	{:else}
		<section class="grid gap-7">
			<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-muted-foreground">
					<ShieldCheckIcon class="size-3.5 text-primary" aria-hidden="true" />
					end-to-end encrypted · burns after read
				</p>
				<!-- The heading adapts to the delivery choice: each tab names its own flow,
				     and the sublink under it is that flow's other entry point. -->
				<h1 class="page-title">{delivery === 'code' ? 'Send a secret' : 'Create a secret'}</h1>
				{#if delivery === 'code'}
					<p class="text-sm text-muted-foreground">
						Received a 6-digit code?
						<button
							type="button"
							class="font-medium text-primary underline underline-offset-4 hover:text-primary/80"
							onclick={() => {
								receiveModalOpen = true;
							}}
						>Enter it here</button>
					</p>
				{:else}
					<p class="text-sm text-muted-foreground">
						Need one from someone else?
						<a
							class="font-medium text-primary underline underline-offset-4 hover:text-primary/80"
							href={resolve('request')}
						>Request a secret</a>
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
					<!-- P2 stage card: the form folds away while the create runs.
					     Cancel only stops the encrypted PUT; finalize has its own status. -->
					<CreateStageCard
						steps={stageSteps}
						step={stageStep}
						status={status}
						progress={stageCardProgress}
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
						<Label for="secret-text" class="text-sm font-medium">Message</Label>
						<Textarea
							id="secret-text"
							class="min-h-44 resize-y px-3.5 py-3"
							placeholder="Paste text"
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
						<span class="text-sm font-medium">Files</span>
						<!-- Label wraps the input, so a click opens the picker natively and a
							     drop lands files without any wiring; focus-within surfaces the
							     visually-hidden input's keyboard focus on the zone. -->
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<label
							class={cn(
								'grid cursor-pointer place-items-center gap-2 rounded-lg border border-dashed px-4 py-8 text-center transition-colors focus-within:border-primary focus-within:ring-2 focus-within:ring-ring',
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
							<FileUpIcon
								class={cn('size-6', dragActive ? 'text-primary' : 'text-muted-foreground')}
								aria-hidden="true"
							/>
							<p class="text-sm">
								<span class="font-medium text-foreground">Drop files</span>
									<span class="text-muted-foreground"> or click to browse</span>
							</p>
							<p class="micro text-muted-foreground">
								<!-- micro uppercases its text; byte units are case-significant (MiB, KiB). -->
								Multiple files are zipped into one · up to 
								<span class="normal-case">{formatBytes(limits.maxFileBytes)}</span>
							</p>
							<input
								bind:this={fileInput}
								id="secret-file"
								type="file"
								multiple
								class="sr-only"
								aria-label="Add files to upload"
								disabled={isCreating}
								onchange={onFileInputChange}
							/>
						</label>
						{#if pickedFiles.length > 0}
							<ul class="grid gap-1.5">
								{#each pickedFiles as file, index (fileKey(file))}
									<li
										class="flex min-h-9 items-center justify-between gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-sm"
									>
										<span class="truncate text-muted-foreground">{file.name}</span>
										<span class="flex shrink-0 items-center gap-2">
												<span class="font-mono text-xs text-muted-foreground">
													{formatBytes(file.size)}
												</span>
											<button
												type="button"
												class="grid size-5 place-items-center rounded text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
												aria-label={`Remove ${file.name}`}
												disabled={isCreating}
												onclick={() => {
													removeFile(index);
												}}
												>
													<XIcon class="size-4" aria-hidden="true" />
												</button>
										</span>
									</li>
								{/each}
							</ul>
							{#if bundling}
								<p
									class="micro text-right text-muted-foreground"
									role="status"
									aria-live="polite"
									>
										Zipping {pickedFiles.length} files…
									</p>
							{:else if pickedFiles.length > 1 && selectedFile}
									<p class="micro text-right normal-case text-muted-foreground">
										{pickedFiles.length} files → {selectedFile.name} · {formatBytes(selectedFile.size)}
									</p>
							{/if}
						{/if}
						{#if selectedFileTooLarge}
								<p class="text-sm text-destructive" role="alert" aria-live="assertive">
									Choose files up to {formatBytes(limits.maxFileBytes)} total.
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
						<span class="text-sm font-medium">{modeLabel(mode)}</span>
							<CredentialForm bind:envelope={credentialEnvelope} disabled={isCreating} />
					</div>
				{/if}

				{#if delivery === 'link'}
					<!-- U2 switch row: the always-visible paragraph moved behind the
					     info icon (hover or keyboard focus). Not "end-to-end" here:
					     the API issues a code transfer's code and salt, so only a
					     passphrase keeps that transfer from the server. -->
					<SwitchRow
						id="use-passphrase"
						bind:checked={usePassphrase}
						label="Protect with an additional passphrase"
						icon={LockKeyholeIcon}
						infoLabel="Why the server can't see this secret"
						describedBy="passphrase-privacy"
						disabled={isCreating}
					>
						{#snippet details()}
							<p class="mb-1 text-xs font-medium text-foreground">Why we can't read your secret</p>
							<p class="text-[0.8125rem] leading-snug text-muted-foreground">It is locked on your device before sending.</p>
							<p class="text-[0.8125rem] leading-snug text-muted-foreground">We never receive the passphrase.</p>
						{/snippet}
						{#snippet field()}
							<!-- Single inline field: the placeholder is the visible name
							     (the row above already made "additional" clear), and both
							     in-field icons keep the 44px row-height tap target. -->
							<div class="relative">
								<Input
									id="secret-passphrase"
									name="flick-passphrase"
									type={revealPassphrase ? 'text' : 'password'}
									placeholder="Add a passphrase"
									aria-label="Additional passphrase"
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
										aria-label="Generate passphrase"
										title="Generate passphrase"
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
						{/snippet}
					</SwitchRow>

					<div class="grid gap-2.5">
						<span class="flex items-center gap-1.5 text-sm font-medium">
							<ClockIcon class="size-4" aria-hidden="true" />
							Expires after
						</span>
						<LifetimePicker bind:seconds={ttlSeconds} disabled={isCreating} groupLabel="Secret lifetime" label="" />
					</div>

					<div class="grid gap-3">
						<Button
							type="submit"
							variant="toggleActive"
							size="cta"
							disabled={!canCreate}
							class={BLOCKED_CLASS}
							title={!canCreate ? blockedTitle : undefined}
						>
							{isCreating ? 'Creating' : 'Create link'}
						</Button>
						<p class="micro -mt-2 flex items-center justify-center gap-1.5 text-muted-foreground">
							<FlameIcon class="size-3" aria-hidden="true" />
							<span>burns after a single open</span>
						</p>
					</div>
				{:else}
					<SwitchRow
						id="require-confirmation"
						bind:checked={pipeRequireApproval}
						label="Require confirmation before transfer"
						icon={ShieldCheckIcon}
						infoLabel="How confirmation works"
						describedBy="confirmation-procedure"
						disabled={isCreating}
					>
						{#snippet details()}
							<p class="mb-1 text-xs font-medium text-foreground">How confirmation works</p>
							<ol class="list-decimal space-y-1 pl-4 text-[0.8125rem] text-muted-foreground marker:text-foreground/60">
								<li>Both screens show the same 3-digit number.</li>
								<li>Check that they match.</li>
								<li>Tap "Approve &amp; send".</li>
							</ol>
						{/snippet}
					</SwitchRow>

					<div class="grid gap-3">
						<!-- Round 22: the same solid black as the link tab's Create
						     link — this is the tab's primary action. -->
						<Button
							type="button"
							variant="toggleActive"
							size="cta"
							disabled={!canPipe}
							class={BLOCKED_CLASS}
							title={!canPipe ? blockedTitle : undefined}
							onclick={startPipeSend}
						>
							{isCreating ? 'Creating' : 'Start live transfer'}
						</Button>
						<p class="micro flex items-center justify-center gap-1.5 text-muted-foreground">
							<span>fixed 3-minute in-memory relay · 0B storage</span>
						</p>
					</div>
				{/if}
			{/if}

			<!-- After a failed or cancelled create the error lands on the
			     returned form; during creation the status lives in the stage card. -->
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

	<!-- Y1: guides open in a new tab so the in-progress form survives. -->
	<nav
		aria-label="Sharing guides"
		class="flex flex-wrap justify-center gap-x-5 gap-y-2 border-t pt-3.5 text-[0.8125rem] text-muted-foreground"
	>
		<a
			target="_blank"
			rel="noopener noreferrer"
			href={resolve('guides/password-sharing/')}
			class="inline-flex items-center gap-1 py-2 underline-offset-4 hover:underline"
		>How password sharing works
			<ArrowUpRightIcon class="size-3" aria-hidden="true" />
		</a>

		<a
			target="_blank"
			rel="noopener noreferrer"
			href={resolve('guides/temporary-file-sharing/')}
			class="inline-flex items-center gap-1 py-2 underline-offset-4 hover:underline"
		>How temporary file sharing works
			<ArrowUpRightIcon class="size-3" aria-hidden="true" />
		</a>
	</nav>
</DeliveryFrame>

<QrModal bind:open={qrOpen} url={shareUrl} />

<!-- Opened from the code tab's "Enter it here" sublink. SiteHeader keeps its own
     instance for the #code= deep link; at most one is open at a time. -->
<ReceiveCodeModal
	bind:open={receiveModalOpen}
	initialCode=""
	apiBaseUrl={DEFAULT_API_BASE_URL}
/>
