<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		createSecretAPIClient,
		createShareURL,
		SecretAPIError,
		type TTLSeconds
	} from '$lib/api/secrets';
	import CredentialForm from '$lib/components/CredentialForm.svelte';
	import { createAccessVerifier, encryptFile, encryptText } from '$lib/crypto/text';
	import {
		buildEnvelope,
		CREDENTIAL_TEMPLATES,
		CREDENTIAL_TYPES,
		serializeCredential,
		type CredentialEnvelope,
		type CredentialType
	} from '$lib/credentials';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import {
		CheckIcon,
		CopyIcon,
		CreditCardIcon,
		ExternalLinkIcon,
		FileUpIcon,
		IdCardIcon,
		KeyRoundIcon,
		ListPlusIcon,
		LockKeyholeIcon,
		TypeIcon
	} from '@lucide/svelte';

	type StatusKind = 'idle' | 'success' | 'error';
	type CreateMode = 'text' | 'file' | CredentialType;

	const ttlOptions: Array<{ label: string; value: TTLSeconds }> = [
		{ label: '10 min', value: 600 },
		{ label: '1 hour', value: 3600 },
		{ label: '24 hours', value: 86400 }
	];

	const defaultLocalFileMaxBytes = 1024 * 1024 - 16;
	const configuredLocalFileMaxBytes = Number(
		import.meta.env.PUBLIC_BURNLINK_LOCAL_FILE_MAX_BYTES ?? defaultLocalFileMaxBytes
	);
	const localFileMaxBytes =
		Number.isFinite(configuredLocalFileMaxBytes) && configuredLocalFileMaxBytes > 0
			? configuredLocalFileMaxBytes
			: defaultLocalFileMaxBytes;
	const api = createSecretAPIClient();
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
	let ttlSeconds = $state<TTLSeconds>(600);
	let selectedFiles = $state<FileList>();
	let selectedFile = $state<File | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let shareURL = $state('');
	let expiresAt = $state('');
	let status = $state('');
	let statusKind = $state<StatusKind>('idle');
	let isCreating = $state(false);
	let copyState = $state<'idle' | 'copied'>('idle');

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
	const canCreate = $derived(hasCreatePayload && passphrase.length > 0 && !isCreating);
	const hasResult = $derived(shareURL.length > 0);

	function submitCreate(event: SubmitEvent): void {
		event.preventDefault();
		void createSecret();
	}

	async function createSecret(): Promise<void> {
		if (!canCreate) return;

		isCreating = true;
		status = 'Encrypting';
		statusKind = 'idle';
		copyState = 'idle';

		try {
			const access = await createAccessVerifier(passphrase);
			const created = await createSelectedSecret(access);

			shareURL = createShareURL(window.location.origin, created.id);
			expiresAt = created.expires_at;
			plaintext = '';
			passphrase = '';
			clearSelectedFile();
			if (isCredentialMode(mode)) {
				credentialEnvelope = buildEnvelope(mode);
			}
			status = `${modeLabel(mode)} secret created`;
			statusKind = 'success';
		} catch (error) {
			status = error instanceof SecretAPIError ? error.message : 'Could not create link. Try again.';
			statusKind = 'error';
		} finally {
			isCreating = false;
		}
	}

	async function createSelectedSecret(
		access: Awaited<ReturnType<typeof createAccessVerifier>>
	): Promise<{ id: string; expires_at: string }> {
		if (mode === 'text') {
			return api.createTextSecret(await encryptText(plaintext, passphrase), ttlSeconds, access);
		}

		if (mode === 'file') {
			return api.createFileSecret(
				await encryptFile(requireSelectedFile(), passphrase),
				ttlSeconds,
				access
			);
		}

		return api.createTextSecret(
			await encryptText(serializeCredential(credentialEnvelope), passphrase),
			ttlSeconds,
			access
		);
	}

	async function copyShareURL(): Promise<void> {
		if (shareURL.length === 0) return;

		try {
			await navigator.clipboard.writeText(shareURL);
			copyState = 'copied';
			window.setTimeout(() => {
				copyState = 'idle';
			}, 1600);
		} catch {
			status = 'Could not copy link.';
			statusKind = 'error';
		}
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

	function formatExpiresAt(value: string): string {
		if (value.length === 0) return 'Not created';
		return new Intl.DateTimeFormat(undefined, {
			dateStyle: 'medium',
			timeStyle: 'short'
		}).format(new Date(value));
	}

	function formatBytes(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
		return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
	}

	function isCredentialMode(value: CreateMode): value is CredentialType {
		const credentialTypes: readonly string[] = CREDENTIAL_TYPES;
		return credentialTypes.includes(value);
	}

	function modeLabel(value: CreateMode): string {
		if (value === 'text') return 'Text';
		if (value === 'file') return 'File';
		return CREDENTIAL_TEMPLATES.find((template) => template.type === value)?.label ?? value;
	}

	function credentialIcon(icon: string): typeof ListPlusIcon {
		return credentialIconComponents[icon as keyof typeof credentialIconComponents] ?? ListPlusIcon;
	}
</script>

<svelte:head>
	<title>Create secret - BurnLink</title>
</svelte:head>

<main class="min-h-screen bg-background px-3 py-4 text-foreground sm:px-5 sm:py-6">
	<div class="mx-auto grid w-full max-w-xl gap-4">
		<header class="flex items-center justify-between gap-3">
			<a class="inline-flex w-fit items-center gap-2 text-sm font-semibold" href={resolve('/')}>
				<span class="inline-flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
					<LockKeyholeIcon class="size-4" />
				</span>
				<span>BurnLink</span>
			</a>

			<nav class="flex items-center gap-2">
				<a class={buttonVariants({ size: 'sm' })} href={resolve('/')} aria-current="page">Create</a>
			</nav>
		</header>

		<section class="grid gap-4">
			<Card.Card class="rounded-lg">
				<Card.Header class="border-b px-4 py-4 sm:px-5">
					<Card.Title class="text-xl">Create secret</Card.Title>
				</Card.Header>
				<Card.Content class="px-4 py-4 sm:px-5">
					<form class="grid gap-4" autocomplete="off" onsubmit={submitCreate}>
						<div class="grid gap-2">
							<Label>Type</Label>
							<div class="flex flex-wrap gap-1.5" role="group" aria-label="Secret type">
								{#each baseModeOptions as option (option.type)}
									{@const Icon = option.icon}
									<Button
										type="button"
										variant={mode === option.type ? 'default' : 'outline'}
										class="h-9 flex-1 basis-[5.5rem] px-2"
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
										variant={mode === template.type ? 'default' : 'outline'}
										class="h-9 flex-1 basis-[5.5rem] px-2"
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
							<div class="grid gap-2">
								<Label for="secret-text">Secret</Label>
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
							<div class="grid gap-2">
								<Label for="secret-file">File</Label>
								<Input
									id="secret-file"
									type="file"
									bind:ref={fileInput}
									bind:files={selectedFiles}
									disabled={isCreating}
									onchange={syncSelectedFile}
								/>
								<div class="flex min-h-9 items-center justify-between gap-3 rounded-md border border-border bg-muted/30 px-3 py-2 text-sm">
									<span class="truncate text-muted-foreground">
										{selectedFile ? selectedFile.name : 'No file selected'}
									</span>
									{#if selectedFile}
										<span class="shrink-0 text-xs text-muted-foreground">
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
							<CredentialForm bind:envelope={credentialEnvelope} disabled={isCreating} />
						{/if}

						<div class="grid gap-2">
							<Label for="secret-passphrase">Passphrase</Label>
							<Input
								id="secret-passphrase"
								name="burnlink-passphrase"
								type="text"
								class="passphrase-mask"
								autocomplete="off"
								autocapitalize="none"
								spellcheck="false"
								data-1p-ignore="true"
								data-bwignore="true"
								data-lpignore="true"
								placeholder="Required"
								bind:value={passphrase}
								disabled={isCreating}
								required
							/>
						</div>

						<div class="grid gap-2">
							<Label>Expires</Label>
							<div
								class="grid grid-cols-3 gap-1 rounded-md border border-border bg-muted/30 p-1"
								role="group"
								aria-label="Secret lifetime"
							>
								{#each ttlOptions as option (option.value)}
									<Button
										type="button"
										variant={ttlSeconds === option.value ? 'default' : 'ghost'}
										class="h-10 px-1 text-xs sm:text-sm"
										aria-pressed={ttlSeconds === option.value}
										disabled={isCreating}
										onclick={() => {
											ttlSeconds = option.value;
										}}
									>{option.label}</Button>
								{/each}
							</div>
						</div>

						<div class="grid gap-3">
							<Button type="submit" class="h-10 w-full" disabled={!canCreate}>
								<LockKeyholeIcon class="size-4" />
								{isCreating ? 'Creating' : 'Create link'}
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
						</div>

						{#if hasResult}
							<div class="grid gap-4 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-emerald-950 sm:p-4">
								<div class="flex items-start gap-3">
									<CheckIcon class="mt-0.5 size-4 shrink-0" />
									<div class="grid gap-1">
										<p class="text-sm font-medium">Ready</p>
										<p class="text-sm text-emerald-800">Expires {formatExpiresAt(expiresAt)}</p>
									</div>
								</div>

								<div class="grid gap-2">
									<Label for="share-url">Share URL</Label>
									<div class="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
										<Input id="share-url" value={shareURL} readonly />
										<Button
											type="button"
											variant="outline"
											size="icon"
											aria-label="Copy share URL"
											title="Copy share URL"
											onclick={copyShareURL}
										>
											{#if copyState === 'copied'}
												<CheckIcon class="size-4" />
											{:else}
												<CopyIcon class="size-4" />
											{/if}
										</Button>
									</div>
								</div>

								<div class="grid gap-2 text-sm">
									<a
										class={buttonVariants({ variant: 'outline' }) + ' w-full border-emerald-300 bg-white/70'}
										href={shareURL}
										rel="external"
									>
										<ExternalLinkIcon class="size-4" />
										Open link
									</a>
								</div>
							</div>
						{/if}
					</form>
				</Card.Content>
			</Card.Card>
		</section>
	</div>
</main>

<style>
	.passphrase-mask {
		-webkit-text-security: disc;
	}
</style>
