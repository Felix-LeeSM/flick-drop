<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		createSecretAPIClient,
		createShareURL,
		type TTLSeconds
	} from '$lib/api/secrets';
	import { createAccessVerifier, encryptText } from '$lib/crypto/text';
	import { Alert, AlertDescription, AlertTitle } from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Label } from '$lib/components/ui/label';
	import { Separator } from '$lib/components/ui/separator';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Input } from '$lib/components/ui/input';
	import {
		CheckIcon,
		CopyIcon,
		ExternalLinkIcon,
		KeyRoundIcon,
		LockKeyholeIcon,
		TimerIcon
	} from '@lucide/svelte';

	type StatusKind = 'idle' | 'success' | 'error';

	const ttlOptions: Array<{ label: string; value: TTLSeconds }> = [
		{ label: '10 min', value: 600 },
		{ label: '1 hour', value: 3600 },
		{ label: '24 hours', value: 86400 }
	];

	const api = createSecretAPIClient();

	let plaintext = $state('');
	let passphrase = $state('');
	let ttlSeconds = $state<TTLSeconds>(600);
	let shareURL = $state('');
	let createdID = $state('');
	let expiresAt = $state('');
	let status = $state('');
	let statusKind = $state<StatusKind>('idle');
	let isCreating = $state(false);
	let copyState = $state<'idle' | 'copied'>('idle');

	const canCreate = $derived(plaintext.trim().length > 0 && passphrase.length > 0 && !isCreating);
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
			const payload = await encryptText(plaintext, passphrase);
			const access = await createAccessVerifier(passphrase);
			const created = await api.createTextSecret(payload, ttlSeconds, access);

			shareURL = createShareURL(window.location.origin, created.id);
			createdID = created.id;
			expiresAt = created.expires_at;
			plaintext = '';
			passphrase = '';
			status = 'Secret created';
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

	function formatExpiresAt(value: string): string {
		if (value.length === 0) return 'Not created';
		return new Intl.DateTimeFormat(undefined, {
			dateStyle: 'medium',
			timeStyle: 'short'
		}).format(new Date(value));
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
					<form class="grid gap-5" onsubmit={submitCreate}>
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

						<div class="grid gap-2">
							<Label for="secret-passphrase">Passphrase</Label>
							<Input
								id="secret-passphrase"
								type="password"
								autocomplete="new-password"
								placeholder="Required"
								bind:value={passphrase}
								disabled={isCreating}
								required
							/>
						</div>

						<div class="grid gap-2">
							<Label>Expires</Label>
							<div class="grid grid-cols-3 gap-2" role="group" aria-label="Secret lifetime">
								{#each ttlOptions as option (option.value)}
									<Button
										type="button"
										variant={ttlSeconds === option.value ? 'default' : 'outline'}
										aria-pressed={ttlSeconds === option.value}
										disabled={isCreating}
										onclick={() => {
											ttlSeconds = option.value;
										}}
									>
										<TimerIcon class="size-4" />
										{option.label}
									</Button>
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
					</form>
				</Card.Content>
			</Card.Card>

			<div class="grid gap-5 content-start">
				<Card.Card class="rounded-lg">
					<Card.Header class="border-b">
						<Card.Title class="text-base">Link</Card.Title>
					</Card.Header>
					<Card.Content class="grid gap-4">
						{#if hasResult}
							<Alert class="border-emerald-200 bg-emerald-50 text-emerald-950">
								<CheckIcon class="size-4" />
								<AlertTitle>Ready</AlertTitle>
								<AlertDescription class="text-emerald-800">
									Expires {formatExpiresAt(expiresAt)}
								</AlertDescription>
							</Alert>

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
								<div class="flex items-center justify-between gap-3">
									<span class="text-muted-foreground">Secret ID</span>
									<code class="max-w-48 truncate rounded-md bg-muted px-2 py-1 text-xs">{createdID}</code>
								</div>
								<Separator />
								<a
									class={buttonVariants({ variant: 'outline' }) + ' w-full'}
									href={shareURL}
									rel="external"
								>
									<ExternalLinkIcon class="size-4" />
									Open link
								</a>
							</div>
						{:else}
							<div class="rounded-lg border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
								No link yet
							</div>
						{/if}
					</Card.Content>
				</Card.Card>
			</div>
		</section>
	</div>
</main>
