<script lang="ts">
import {
	ClockIcon,
	CreditCardIcon,
	EyeIcon,
	EyeOffIcon,
	FileUpIcon,
	FlameIcon,
	IdCardIcon,
	KeyRoundIcon,
	ListPlusIcon,
	LockKeyholeIcon,
	QrCodeIcon,
	ShieldCheckIcon,
	TypeIcon
} from '@lucide/svelte';
import { resolve } from '$app/paths';
import {
	type CreateSecretResponse,
	createSecretApiClient,
	createShareUrl,
	SecretApiError,
	type TtlSeconds
} from '$lib/api/secrets';
import CredentialForm from '$lib/components/CredentialForm.svelte';
import QrModal from '$lib/components/QrModal.svelte';
import SuccessCheck from '$lib/components/SuccessCheck.svelte';
import ThemeToggle from '$lib/components/ThemeToggle.svelte';
import UrlField from '$lib/components/UrlField.svelte';
import { Button, buttonVariants } from '$lib/components/ui/button';
import { Checkbox } from '$lib/components/ui/checkbox';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
import { Textarea } from '$lib/components/ui/textarea';
import {
	buildEnvelope,
	CREDENTIAL_TEMPLATES,
	CREDENTIAL_TYPES,
	type CredentialEnvelope,
	type CredentialType,
	serializeCredential
} from '$lib/credentials';
import {
	createAccessVerifier,
	encryptFile,
	encryptFileWithKey,
	encryptText,
	encryptTextWithKey,
	generateSecretKey
} from '$lib/crypto/text';

type StatusKind = 'idle' | 'encrypting' | 'error';
type CreateMode = 'text' | 'file' | CredentialType;

const MIN_TTL_SECONDS = 300;
const MAX_TTL_SECONDS = 604_800;
const ttlUnitFactor: Record<'minutes' | 'hours' | 'days', number> = {
	minutes: 60,
	hours: 3600,
	days: 86_400
};
const ttlPresets: Array<{ label: string; value: number }> = [
	{ label: '10 min', value: 600 },
	{ label: '1 hour', value: 3600 },
	{ label: '24 hours', value: 86_400 },
	{ label: '7 days', value: 604_800 }
];

const defaultLocalFileMaxBytes = 1024 * 1024 - 16;
const configuredLocalFileMaxBytes = Number(
	import.meta.env.PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES ?? defaultLocalFileMaxBytes
);
const localFileMaxBytes =
	Number.isFinite(configuredLocalFileMaxBytes) && configuredLocalFileMaxBytes > 0
		? configuredLocalFileMaxBytes
		: defaultLocalFileMaxBytes;
const api = createSecretApiClient();
const baseModeOptions = [
	{ type: 'text', label: 'Text', icon: TypeIcon },
	{ type: 'file', label: 'File', icon: FileUpIcon }
] as const;
const credentialIconComponents = {
	'key-round': KeyRoundIcon,
	'credit-card': CreditCardIcon,
	'id-card': IdCardIcon,
	'list-plus': ListPlusIcon
};

let mode = $state<CreateMode>('text');
let plaintext = $state('');
let credentialEnvelope = $state<CredentialEnvelope>(buildEnvelope('login'));
let passphrase = $state('');
// Model A (true) derives key + access proof from a passphrase; Model B (false)
// generates a random key carried in the URL fragment. See security-model.md.
let usePassphrase = $state(true);
let revealPassphrase = $state(false);
let presetSeconds = $state(600);
let customActive = $state(false);
let customValue = $state(2);
let customUnit = $state<'minutes' | 'hours' | 'days'>('days');
const ttlSeconds = $derived(customActive ? customValue * ttlUnitFactor[customUnit] : presetSeconds);
let selectedFiles = $state<FileList>();
let selectedFile = $state<File | null>(null);
let fileInput = $state<HTMLInputElement | null>(null);
let shareUrl = $state('');
let expiresAt = $state('');
let status = $state('');
let statusKind = $state<StatusKind>('idle');
let isCreating = $state(false);
let qrOpen = $state(false);

// 1s tick drives the expiry countdown in the success panel.
let nowTick = $state(Date.now());
$effect(() => {
	const id = window.setInterval(() => {
		nowTick = Date.now();
	}, 1000);
	return () => window.clearInterval(id);
});

const selectedFileTooLarge = $derived(
	selectedFile !== null && selectedFile.size > localFileMaxBytes
);
const hasCredentialPayload = $derived(
	(credentialEnvelope.title ?? '').trim().length > 0 ||
		(credentialEnvelope.notes ?? '').trim().length > 0 ||
		credentialEnvelope.fields.some((field) => field.value.trim().length > 0)
);
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
		ttlSeconds >= MIN_TTL_SECONDS &&
		ttlSeconds <= MAX_TTL_SECONDS &&
		!isCreating
);
const hasResult = $derived(shareUrl.length > 0);
const remainingSeconds = $derived(
	expiresAt.length > 0
		? Math.max(0, Math.round((new Date(expiresAt).getTime() - nowTick) / 1000))
		: 0
);

function submitCreate(event: SubmitEvent): void {
	event.preventDefault();
	void createSecret();
}

async function createSecret(): Promise<void> {
	if (!canCreate) {
		return;
	}

	isCreating = true;
	status = 'Encrypting';
	statusKind = 'encrypting';

	try {
		const { created, key } = await createSelectedSecret();

		shareUrl = createShareUrl(window.location.origin, created.id, key);
		expiresAt = created.expires_at;
		plaintext = '';
		passphrase = '';
		revealPassphrase = false;
		clearSelectedFile();
		if (isCredentialMode(mode)) {
			credentialEnvelope = buildEnvelope(mode);
		}
		status = '';
		statusKind = 'idle';
	} catch (error) {
		status = error instanceof SecretApiError ? error.message : 'Could not create link. Try again.';
		statusKind = 'error';
	} finally {
		isCreating = false;
	}
}

type CreateResult = {
	created: CreateSecretResponse;
	// raw key for Model B; omitted for Model A (passphrase is the key source).
	key?: Uint8Array;
};

async function createSelectedSecret(): Promise<CreateResult> {
	if (usePassphrase) {
		const access = await createAccessVerifier(passphrase);
		if (mode === 'text') {
			return {
				created: await api.createTextSecret(
					await encryptText(plaintext, passphrase),
					ttlSeconds,
					access
				)
			};
		}
		if (mode === 'file') {
			return {
				created: await api.createFileSecret(
					await encryptFile(requireSelectedFile(), passphrase),
					ttlSeconds,
					access
				)
			};
		}
		return {
			created: await api.createTextSecret(
				await encryptText(serializeCredential(credentialEnvelope), passphrase),
				ttlSeconds,
				access
			)
		};
	}

	// Model B: random key, no passphrase. The key travels in the URL fragment.
	const { key, raw } = await generateSecretKey();
	if (mode === 'text') {
		return {
			created: await api.createTextSecret(await encryptTextWithKey(plaintext, key), ttlSeconds),
			key: raw
		};
	}
	if (mode === 'file') {
		return {
			created: await api.createFileSecret(
				await encryptFileWithKey(requireSelectedFile(), key),
				ttlSeconds
			),
			key: raw
		};
	}
	return {
		created: await api.createTextSecret(
			await encryptTextWithKey(serializeCredential(credentialEnvelope), key),
			ttlSeconds
		),
		key: raw
	};
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

function syncSelectedFile(): void {
	selectedFile = selectedFiles?.item(0) ?? null;
	status = '';
	statusKind = 'idle';
}

function clearSelectedFile(): void {
	selectedFile = null;
	selectedFiles = undefined;
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
	expiresAt = '';
	status = '';
	statusKind = 'idle';
	qrOpen = false;
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

function credentialIcon(icon: string): typeof ListPlusIcon {
	return credentialIconComponents[icon as keyof typeof credentialIconComponents] ?? ListPlusIcon;
}
</script>

<svelte:head>
	<title>Create secret - Flick</title>
</svelte:head>

<main class="min-h-screen px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl gap-8">
		<header class="flex items-center justify-between gap-3">
			<a class="flex items-center gap-2.5" href={resolve('/')}>
				<span class="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground">
					<LockKeyholeIcon class="size-4" />
				</span>
				<span class="font-serif text-lg leading-none">Flick</span>
			</a>
			<nav class="flex items-center gap-2">
				<ThemeToggle />
				<a
					class={`${buttonVariants({ variant: 'toggleActive', size: 'seg' })} pointer-events-none`}
					href={resolve('/')}
					aria-current="page"
				>
					Create
				</a>
			</nav>
		</header>

		{#if hasResult}
			<section class="grid gap-7">
				<div class="grid place-items-center gap-4 text-center">
					<SuccessCheck />
					<div class="grid gap-2">
						<p class="micro flex items-center justify-center gap-1.5 text-burn">
							<FlameIcon class="size-3.5" />
							one-time link
						</p>
						<h1 class="font-serif text-3xl sm:text-4xl">Link created</h1>
						<p class="text-sm text-muted-foreground">Opens once, then burns. Forward it fast.</p>
					</div>
					<div class="flex items-center gap-2 font-mono text-sm text-muted-foreground">
						<ClockIcon class="size-4" />
						<span>EXPIRES IN</span>
						<span class="tabular-nums text-foreground">{formatRemaining(remainingSeconds)}</span>
					</div>
				</div>

				<div class="grid gap-3">
					<UrlField value={shareUrl} id="share-url" />
					<Button
						type="button"
						variant="outline"
						class="h-11 w-full"
						onclick={() => {
							qrOpen = true;
						}}
					>
						<QrCodeIcon class="size-4" />
						Show QR
					</Button>
					<Button type="button" variant="ghost" class="h-9 w-full text-sm" onclick={createAnother}>
						Create another
					</Button>
				</div>
			</section>
		{:else}
			<section class="grid gap-7">
				<div class="grid gap-1.5">
					<p class="micro flex items-center gap-1.5 text-muted-foreground">
						<ShieldCheckIcon class="size-3.5" />
						end-to-end encrypted · burns after read
					</p>
					<h1 class="font-serif text-3xl sm:text-4xl">Create a secret</h1>
				</div>

				<form class="grid gap-5" autocomplete="off" onsubmit={submitCreate}>
					<div class="grid gap-2.5">
						<span class="micro text-muted-foreground">type</span>
						<div class="flex flex-wrap gap-2" role="group" aria-label="Secret type">
							{#each baseModeOptions as option (option.type)}
								{@const Icon = option.icon}
								<Button
									type="button"
									variant={mode === option.type ? 'toggleActive' : 'toggle'}
									size="seg"
									aria-pressed={mode === option.type}
									disabled={isCreating}
									onclick={() => {
										switchMode(option.type);
									}}
								>
									<Icon class="size-4" />
									{option.label}
								</Button>
							{/each}
							{#each CREDENTIAL_TEMPLATES as template (template.type)}
								{@const Icon = credentialIcon(template.icon)}
								<Button
									type="button"
									variant={mode === template.type ? 'toggleActive' : 'toggle'}
									size="seg"
									aria-pressed={mode === template.type}
									disabled={isCreating}
									onclick={() => {
										switchMode(template.type);
									}}
								>
									<Icon class="size-4" />
									{template.label}
								</Button>
							{/each}
						</div>
					</div>

					{#if mode === 'text'}
						<div class="grid gap-2.5">
							<Label for="secret-text" class="micro font-normal text-muted-foreground">
								payload
							</Label>
							<Textarea
								id="secret-text"
								class="min-h-48 resize-y"
								placeholder="Paste text"
								bind:value={plaintext}
								disabled={isCreating}
								required
							/>
						</div>
					{:else if mode === 'file'}
						<div class="grid gap-2.5">
							<Label for="secret-file" class="micro font-normal text-muted-foreground">
								payload
							</Label>
							<Input
								id="secret-file"
								type="file"
								bind:ref={fileInput}
								bind:files={selectedFiles}
								disabled={isCreating}
								onchange={syncSelectedFile}
							/>
							<div
								class="flex min-h-9 items-center justify-between gap-3 rounded-md border border-border bg-muted/30 px-3 py-2 text-sm"
							>
								<span class="truncate text-muted-foreground">
									{selectedFile ? selectedFile.name : 'No file selected'}
								</span>
								{#if selectedFile}
									<span class="shrink-0 font-mono text-xs text-muted-foreground">
										{formatBytes(selectedFile.size)}
									</span>
								{/if}
							</div>
							{#if selectedFileTooLarge}
								<p class="text-sm text-destructive">
									Choose a file up to {formatBytes(localFileMaxBytes)}.
								</p>
							{/if}
						</div>
					{:else}
						<div class="grid gap-2.5">
							<span class="micro text-muted-foreground">{modeLabel(mode)}</span>
							<CredentialForm bind:envelope={credentialEnvelope} disabled={isCreating} />
						</div>
					{/if}

					<div class="grid gap-2.5">
						<div class="flex items-center justify-between">
							<span class="micro text-muted-foreground">passphrase</span>
							<span class="micro text-muted-foreground/70">
								{usePassphrase ? 'model A' : 'model B'}
							</span>
						</div>
						<div class="flex min-h-9 items-center gap-2">
							<Checkbox id="use-passphrase" bind:checked={usePassphrase} disabled={isCreating} />
							<Label for="use-passphrase" class="text-sm font-medium">Protect with passphrase</Label>
						</div>
						{#if usePassphrase}
							<div class="relative">
								<Input
									id="secret-passphrase"
									name="flick-passphrase"
									type={revealPassphrase ? 'text' : 'password'}
									autocomplete="off"
									autocapitalize="none"
									spellcheck="false"
									data-1p-ignore="true"
									data-bwignore="true"
									data-lpignore="true"
									class="h-11 pr-11"
									placeholder="Required"
									bind:value={passphrase}
									disabled={isCreating}
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
										<EyeOffIcon class="size-4" />
									{:else}
										<EyeIcon class="size-4" />
									{/if}
								</Button>
							</div>
						{:else}
							<p class="text-sm text-muted-foreground">
								Anyone with the link can open this once. The decryption key is embedded in the URL
								fragment.
							</p>
						{/if}
					</div>

					<div class="grid gap-2.5">
						<span class="micro text-muted-foreground">lifetime</span>
						<div class="flex flex-wrap items-center gap-2" role="group" aria-label="Secret lifetime">
							{#each ttlPresets as option (option.value)}
								<Button
									type="button"
									variant={!customActive && ttlSeconds === option.value ? 'toggleActive' : 'toggle'}
									size="pill"
									aria-pressed={!customActive && ttlSeconds === option.value}
									disabled={isCreating}
									onclick={() => {
										presetSeconds = option.value;
										customActive = false;
									}}
								>
									{#if !customActive && ttlSeconds === option.value}
										<ClockIcon class="size-3.5" />
									{/if}
									{option.label}
								</Button>
							{/each}
							<div
								class={`${buttonVariants({
									variant: customActive ? 'toggleActive' : 'toggle',
									size: 'pill'
								})} px-1.5`}
							>
								<input
									type="text"
									inputmode="numeric"
									placeholder="2"
									value={customValue > 0 ? customValue : ''}
									class="w-7 border-0 bg-transparent p-0 text-center font-mono text-xs leading-none outline-none sm:text-sm"
									disabled={isCreating}
									onfocus={() => (customActive = true)}
									oninput={(event) => {
										customActive = true;
										const digits = event.currentTarget.value.replace(/\D/g, '');
										event.currentTarget.value = digits;
										customValue = digits === '' ? 0 : Number(digits);
									}}
								/>
								<select
									bind:value={customUnit}
									class="cursor-pointer appearance-none border-0 bg-transparent p-0 font-mono text-xs leading-none outline-none sm:text-sm"
									onchange={() => (customActive = true)}
								>
									<option value="minutes">min</option>
									<option value="hours">hours</option>
									<option value="days">days</option>
								</select>
							</div>
						</div>
						{#if ttlSeconds < MIN_TTL_SECONDS || ttlSeconds > MAX_TTL_SECONDS}
							<p class="text-sm text-destructive">
								Choose a lifetime between 5 minutes and 7 days.
							</p>
						{/if}
					</div>

					<div class="grid gap-3">
						<Button
							type="submit"
							class="h-11 w-full shadow-lg shadow-primary/25"
							disabled={!canCreate}
						>
							<LockKeyholeIcon class="size-4" />
							{isCreating ? 'Creating' : 'Create link'}
						</Button>
						{#if status.length > 0}
							<p
								class="text-sm"
								class:text-muted-foreground={statusKind === 'encrypting'}
								class:text-destructive={statusKind === 'error'}
							>
								{status}
							</p>
						{/if}
						<p
							class="micro flex items-center justify-center gap-1.5"
							style="color: color-mix(in oklch, var(--burn) 70%, var(--muted-foreground))"
						>
							<FlameIcon class="size-3.5" />
							burns after a single open
						</p>
					</div>
				</form>
			</section>
		{/if}
	</div>
</main>

<QrModal bind:open={qrOpen} url={shareUrl} />
