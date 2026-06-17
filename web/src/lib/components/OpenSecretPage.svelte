<script lang="ts">
	import { resolve } from '$app/paths';
	import { createSecretAPIClient, type SecretKind } from '$lib/api/secrets';
	import {
		decryptFile,
		decryptText,
		deriveAccessProof,
		type EncryptedFilePayload,
		type EncryptedTextPayload
	} from '$lib/crypto/text';
	import { onDestroy } from 'svelte';
	import { Alert, AlertDescription, AlertTitle } from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import {
		CheckIcon,
		CopyIcon,
		DownloadIcon,
		EyeIcon,
		FileIcon,
		KeyRoundIcon,
		LockKeyholeIcon,
		LockKeyholeOpenIcon
	} from '@lucide/svelte';

	type Props = {
		secretID: string;
	};

	type StatusKind = 'idle' | 'success' | 'error';

	let { secretID }: Props = $props();

	const api = createSecretAPIClient();

	let passphrase = $state('');
	let openedKind = $state<SecretKind | null>(null);
	let decryptedText = $state('');
	let downloadURL = $state('');
	let downloadFilename = $state('');
	let downloadSize = $state(0);
	let status = $state('');
	let statusKind = $state<StatusKind>('idle');
	let isOpening = $state(false);
	let hasOpened = $state(false);
	let copyState = $state<'idle' | 'copied'>('idle');

	const canOpen = $derived(passphrase.length > 0 && !isOpening && !hasOpened);

	onDestroy(() => {
		revokeDownloadURL();
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
		if (!canOpen) return;

		isOpening = true;
		status = 'Opening';
		statusKind = 'idle';
		decryptedText = '';
		copyState = 'idle';
		revokeDownloadURL();

		try {
			const metadata = await api.getSecretMetadata(secretID);
			const accessProof = await deriveAccessProof(passphrase, metadata.access.kdf);
			const payload = await api.openSecret(secretID, accessProof);

			if (payload.kind === 'file') {
				const file = await decryptFile(payload as EncryptedFilePayload, passphrase);
				const fileBuffer = file.bytes.buffer.slice(
					file.bytes.byteOffset,
					file.bytes.byteOffset + file.bytes.byteLength
				) as ArrayBuffer;
				const blob = new Blob([fileBuffer], { type: file.contentType });
				downloadURL = URL.createObjectURL(blob);
				downloadFilename = file.filename;
				downloadSize = file.bytes.byteLength;
				openedKind = 'file';
			} else {
				decryptedText = await decryptText(payload as EncryptedTextPayload, passphrase);
				openedKind = 'text';
			}

			passphrase = '';
			hasOpened = true;
			status = 'Opened';
			statusKind = 'success';
		} catch (error) {
			status = error instanceof Error ? error.message : 'Failed to open secret';
			statusKind = 'error';
		} finally {
			isOpening = false;
		}
	}

	async function copySecret(): Promise<void> {
		if (decryptedText.length === 0) return;

		try {
			await navigator.clipboard.writeText(decryptedText);
			copyState = 'copied';
			window.setTimeout(() => {
				copyState = 'idle';
			}, 1600);
		} catch (error) {
			status = error instanceof Error ? error.message : 'Failed to copy secret';
			statusKind = 'error';
		}
	}

	function revokeDownloadURL(): void {
		if (downloadURL.length > 0) {
			URL.revokeObjectURL(downloadURL);
		}
		downloadURL = '';
		downloadFilename = '';
		downloadSize = 0;
	}

	function formatBytes(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
		return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
	}
</script>

<svelte:head>
	<title>Open secret - BurnLink</title>
</svelte:head>

<main class="min-h-screen bg-background px-4 py-5 text-foreground sm:px-6 lg:px-8">
	<div class="mx-auto grid w-full max-w-5xl gap-5">
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
					Passphrase required
				</Badge>
				<a class={buttonVariants({ variant: 'outline', size: 'sm' })} href={resolve('/')}>Create</a>
			</nav>
		</header>

		<section class="grid gap-5 lg:grid-cols-[320px_minmax(0,1fr)]">
			<Card.Card class="rounded-lg">
				<Card.Header class="border-b">
					<Card.Title class="text-xl">Open secret</Card.Title>
				</Card.Header>
				<Card.Content>
					<form class="grid gap-5" autocomplete="off" onsubmit={submitOpen}>
						<div class="grid gap-2">
							<Label for="open-passphrase">Passphrase</Label>
							<Input
								id="open-passphrase"
								name="burnlink-open-passphrase"
								type="text"
								class="passphrase-mask"
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

						<Button type="submit" disabled={!canOpen}>
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
				<Card.Header class="border-b">
					<div class="flex items-start justify-between gap-3">
						<Card.Title class="text-xl">Secret</Card.Title>
						{#if hasOpened && openedKind === 'text'}
							<Button
								type="button"
								variant="outline"
								size="sm"
								aria-label="Copy secret"
								title="Copy secret"
								onclick={copySecret}
							>
								{#if copyState === 'copied'}
									<CheckIcon class="size-4" />
									Copied
								{:else}
									<CopyIcon class="size-4" />
									Copy
								{/if}
							</Button>
						{/if}
					</div>
				</Card.Header>
				<Card.Content>
					{#if hasOpened && openedKind === 'text'}
						<Textarea
							class="min-h-80 resize-y font-mono text-sm"
							value={decryptedText}
							readonly
							aria-label="Decrypted secret"
						/>
					{:else if hasOpened && openedKind === 'file'}
						<div class="grid min-h-80 place-items-center rounded-lg border border-border bg-muted/30 p-6 text-center">
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
									href={downloadURL}
									download={downloadFilename}
									rel="external"
								>
									<DownloadIcon class="size-4" />
									Download
								</a>
							</div>
						</div>
					{:else if statusKind === 'error'}
						<Alert variant="destructive">
							<AlertTitle>Not opened</AlertTitle>
							<AlertDescription>{status}</AlertDescription>
						</Alert>
					{:else}
						<div class="grid min-h-80 place-items-center rounded-lg border border-dashed border-border bg-muted/30 p-6 text-center text-sm text-muted-foreground">
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

<style>
	.passphrase-mask {
		-webkit-text-security: disc;
	}
</style>
