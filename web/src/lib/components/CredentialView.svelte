<script lang="ts">
import { CheckIcon, CopyIcon, EyeIcon, EyeOffIcon } from '@lucide/svelte';
import { Badge } from '$lib/components/ui/badge';
import { Button } from '$lib/components/ui/button';
import {
	type CredentialEnvelope,
	type CredentialField,
	getCredentialTemplate
} from '$lib/credentials';

type Props = {
	credential: CredentialEnvelope;
};

let { credential }: Props = $props();

let revealedFields = $state<Record<string, boolean>>({});
let copiedFieldId = $state('');

const template = $derived(getCredentialTemplate(credential.type));
const title = $derived((credential.title ?? '').trim() || template.label);
const hasNotes = $derived((credential.notes ?? '').trim().length > 0);

export function copyText(): string {
	return credential.fields.map((field) => `${field.label}: ${field.value}`).join('\n');
}

async function copyField(field: CredentialField): Promise<void> {
	await navigator.clipboard.writeText(field.value);
	copiedFieldId = field.id;
	window.setTimeout(() => {
		copiedFieldId = '';
	}, 1400);
}

function toggleReveal(fieldId: string): void {
	revealedFields = {
		...revealedFields,
		[fieldId]: !revealedFields[fieldId]
	};
}

function displayValue(field: CredentialField): string {
	if (field.secret && !revealedFields[field.id]) {
		return '••••••••';
	}
	return field.value;
}
</script>

<div class="grid gap-4">
	<div class="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
		<div class="grid min-w-0 gap-1">
			<Badge variant="secondary" class="w-fit">
				{template.label}
			</Badge>
			<h2 class="break-words text-lg font-semibold leading-tight">{title}</h2>
		</div>
	</div>

	<div class="grid gap-2">
		{#each credential.fields as field (field.id)}
			<div class="flex min-w-0 flex-col gap-2 rounded-md border border-border bg-muted/20 p-3 sm:flex-row sm:items-center">
				<div class="min-w-0 text-sm font-medium text-muted-foreground sm:w-28 sm:shrink-0">
					{field.label}
				</div>
				<div class="min-w-0 flex-1 break-all text-base sm:text-sm" title={field.secret ? undefined : field.value}>
					{displayValue(field)}
				</div>
				<div class="flex shrink-0 items-center gap-1">
					{#if field.secret}
						<Button
							type="button"
							variant="ghost"
							size="icon"
							class="size-9"
							aria-label={revealedFields[field.id] ? `Hide ${field.label}` : `Show ${field.label}`}
							title={revealedFields[field.id] ? 'Hide value' : 'Show value'}
							onclick={() => {
								toggleReveal(field.id);
							}}
						>
							{#if revealedFields[field.id]}
								<EyeOffIcon class="size-4" aria-hidden="true" />
							{:else}
								<EyeIcon class="size-4" aria-hidden="true" />
							{/if}
						</Button>
					{/if}
					<Button
						type="button"
						variant="ghost"
						size="icon"
						class="size-9"
						aria-label={`Copy ${field.label}`}
						title={`Copy ${field.label}`}
						onclick={() => {
							void copyField(field);
						}}
					>
						{#if copiedFieldId === field.id}
							<CheckIcon class="size-4" aria-hidden="true" />
						{:else}
							<CopyIcon class="size-4" aria-hidden="true" />
						{/if}
					</Button>
				</div>
			</div>
		{/each}
	</div>

	{#if hasNotes}
		<div class="grid gap-1 rounded-md border border-border bg-muted/30 p-3">
			<p class="text-sm font-medium text-muted-foreground">Notes</p>
			<p class="whitespace-pre-wrap break-words text-sm">{credential.notes}</p>
		</div>
	{/if}
</div>
