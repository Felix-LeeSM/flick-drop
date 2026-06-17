<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		createSecretAPIClient,
		createShareURL,
		type TTLSeconds
	} from '$lib/api/secrets';
	import { createAccessVerifier, encryptFile, encryptText } from '$lib/crypto/text';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import {
		CheckIcon,
		CopyIcon,
		ExternalLinkIcon,
		FileUpIcon,
		KeyRoundIcon,
		LockKeyholeIcon,
		TypeIcon
	} from '@lucide/svelte';

	type StatusKind = 'idle' | 'success' | 'error';
	type CreateMode = 'text' | 'file';
	type SampleCase = {
		label: string;
		mode: CreateMode;
		ttl: TTLSeconds;
		passphrase: string;
		text?: string;
		fileName?: string;
		fileContent?: string;
		contentType?: string;
	};

	const ttlOptions: Array<{ label: string; value: TTLSeconds }> = [
		{ label: '10m', value: 600 },
		{ label: '1h', value: 3600 },
		{ label: '24h', value: 86400 }
	];

	const sampleCases: SampleCase[] = [
		{
			label: 'API token',
			mode: 'text',
			ttl: 600,
			passphrase: 'correct horse battery staple',
			text: 'BURNLINK_API_TOKEN=bl_demo_8wd9vdx8fyk2\nROTATE_BY=2026-06-18T09:00:00Z'
		},
		{
			label: 'DB URL',
			mode: 'text',
			ttl: 3600,
			passphrase: 'demo database passphrase',
			text: 'postgres://burnlink:demo-password@db.internal:5432/app?sslmode=require'
		},
		{
			label: 'Incident file',
			mode: 'file',
			ttl: 86400,
			passphrase: 'demo file passphrase',
			fileName: 'incident-notes.txt',
			contentType: 'text/plain',
			fileContent: [
				'Incident: temporary credential rotation',
				'Window: 2026-06-17 13:00-14:00 KST',
				'Action: rotate webhook token after recipient confirms download'
			].join('\n')
		}
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

	let mode = $state<CreateMode>('text');
	let plaintext = $state('');
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
	const hasCreatePayload = $derived(
		mode === 'text' ? plaintext.trim().length > 0 : selectedFile !== null && !selectedFileTooLarge
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
			const created =
				mode === 'text'
					? await api.createTextSecret(await encryptText(plaintext, passphrase), ttlSeconds, access)
					: await api.createFileSecret(
							await encryptFile(requireSelectedFile(), passphrase),
							ttlSeconds,
							access
						);

			shareURL = createShareURL(window.location.origin, created.id);
			expiresAt = created.expires_at;
			plaintext = '';
			passphrase = '';
			clearSelectedFile();
			status = mode === 'text' ? 'Text secret created' : 'File secret created';
			statusKind = 'success';
		} catch (error) {
			status = error instanceof Error ? error.message : 'Failed to create secret';
			statusKind = 'error';
		} finally {
			isCreating = false;
		}
	}

	async function copyShareURL(): Promise<void> {
		if (shareURL.length === 0) return;

		try {
			await navigator.clipboard.writeText(shareURL);
			copyState = 'copied';
			window.setTimeout(() => {
				copyState = 'idle';
			}, 1600);
		} catch (error) {
			status = error instanceof Error ? error.message : 'Failed to copy link';
			statusKind = 'error';
		}
	}

	function switchMode(nextMode: CreateMode): void {
		mode = nextMode;
		status = '';
		statusKind = 'idle';
		if (nextMode === 'text') {
			clearSelectedFile();
		} else {
			plaintext = '';
		}
	}

	function syncSelectedFile(): void {
		selectedFile = selectedFiles?.item(0) ?? null;
		status = '';
		statusKind = 'idle';
	}

	function applySample(sample: SampleCase): void {
		mode = sample.mode;
		ttlSeconds = sample.ttl;
		passphrase = sample.passphrase;
		shareURL = '';
		expiresAt = '';
		status = '';
		statusKind = 'idle';
		copyState = 'idle';

		if (sample.mode === 'text') {
			plaintext = sample.text ?? '';
			clearSelectedFile();
			return;
		}

		plaintext = '';
		selectedFile = new File([sample.fileContent ?? ''], sample.fileName ?? 'burnlink-sample.txt', {
			type: sample.contentType ?? 'text/plain'
		});
		if (fileInput) {
			fileInput.value = '';
		}
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
			throw new Error('File is required');
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
</script>

<svelte:head>
	<title>Create secret - BurnLink</title>
</svelte:head>

<main class="min-h-screen bg-background px-4 py-5 text-foreground sm:px-6 lg:px-8">
	<div class="mx-auto grid w-full max-w-6xl gap-5">
		<header class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<a class="inline-flex w-fit items-center gap-2 text-sm font-semibold" href={resolve('/')}>
				<span class="inline-flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
					<LockKeyholeIcon class="size-4" />
				</span>
				<span>BurnLink</span>
			</a>

			<nav class="flex flex-wrap items-center gap-2">
				<Badge variant="secondary" class="rounded-md border border-border bg-card">
					<KeyRoundIcon class="size-3" />
					Browser encrypted
				</Badge>
				<a class={buttonVariants({ size: 'sm' })} href={resolve('/')} aria-current="page">Create</a>
			</nav>
		</header>

		<section class="grid gap-5 lg:grid-cols-[minmax(0,1fr)_360px]">
			<Card.Card class="rounded-lg">
				<Card.Header class="border-b">
					<Card.Title class="text-xl">Create secret</Card.Title>
				</Card.Header>
				<Card.Content>
					<form class="grid gap-5" autocomplete="off" onsubmit={submitCreate}>
						<div class="grid gap-2">
							<Label>Type</Label>
							<div class="grid grid-cols-2 gap-2" role="group" aria-label="Secret type">
								<Button
									type="button"
									variant={mode === 'text' ? 'default' : 'outline'}
									aria-pressed={mode === 'text'}
									disabled={isCreating}
									onclick={() => {
										switchMode('text');
									}}
								>
									<TypeIcon class="size-4" />
									Text
								</Button>
								<Button
									type="button"
									variant={mode === 'file' ? 'default' : 'outline'}
									aria-pressed={mode === 'file'}
									disabled={isCreating}
									onclick={() => {
										switchMode('file');
									}}
								>
									<FileUpIcon class="size-4" />
									File
								</Button>
							</div>
						</div>

						{#if mode === 'text'}
							<div class="grid gap-2">
								<Label for="secret-text">Secret</Label>
								<Textarea
									id="secret-text"
									class="min-h-56 resize-y"
									placeholder="Paste text"
									bind:value={plaintext}
									disabled={isCreating}
									required
								/>
							</div>
						{:else}
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
										File must be {formatBytes(localFileMaxBytes)} or smaller for local SQLite storage.
									</p>
								{/if}
							</div>
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
										class="h-8"
										aria-pressed={ttlSeconds === option.value}
										disabled={isCreating}
										onclick={() => {
											ttlSeconds = option.value;
										}}
									>{option.label}</Button>
								{/each}
							</div>
						</div>

						<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
							<Button type="submit" class="w-full sm:w-auto" disabled={!canCreate}>
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
							<div class="grid gap-4 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-emerald-950">
								<div class="flex items-start gap-3">
									<CheckIcon class="mt-0.5 size-4 shrink-0" />
									<div class="grid gap-1">
										<p class="text-sm font-medium">Ready</p>
										<p class="text-sm text-emerald-800">Expires {formatExpiresAt(expiresAt)}</p>
									</div>
								</div>

								<div class="grid gap-2">
									<Label for="share-url">Share URL</Label>
									<div class="flex gap-2">
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

			<div class="grid content-start gap-5">
				<Card.Card class="rounded-lg">
					<Card.Header class="border-b">
						<Card.Title class="text-base">Samples</Card.Title>
					</Card.Header>
					<Card.Content class="grid gap-2">
						{#each sampleCases as sample (sample.label)}
							<Button
								type="button"
								variant="outline"
								class="justify-start"
								disabled={isCreating}
								onclick={() => {
									applySample(sample);
								}}
							>
								{#if sample.mode === 'text'}
									<TypeIcon class="size-4" />
								{:else}
									<FileUpIcon class="size-4" />
								{/if}
								{sample.label}
							</Button>
						{/each}
					</Card.Content>
				</Card.Card>

			</div>
		</section>
	</div>
</main>

<style>
	.passphrase-mask {
		-webkit-text-security: disc;
	}
</style>
