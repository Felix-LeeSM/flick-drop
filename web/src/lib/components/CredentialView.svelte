<script lang="ts">
import { CheckIcon, CopyIcon, EyeIcon, EyeOffIcon } from '@lucide/svelte';
import { Badge } from '#lib/components/ui/badge/index.js';
import { Button } from '#lib/components/ui/button/index.js';
import {
	type CredentialEnvelope,
	type CredentialField,
	credentialTemplateText,
	displayFieldLabel
} from '#lib/credentials/index.js';
import { t } from '#lib/i18n/index.js';

type Props = {
	credential: CredentialEnvelope;
};

let { credential }: Props = $props();

let revealedFields = $state<Record<string, boolean>>({});
let copiedFieldId = $state('');

const templateName = $derived(credentialTemplateText(credential.type, t().credentials).name);
const title = $derived((credential.title ?? '').trim() || templateName);
const hasNotes = $derived((credential.notes ?? '').trim().length > 0);

export function copyText(): string {
	return credential.fields.map((field) => `${fieldLabel(field)}: ${field.value}`).join('\n');
}

async function copyField(field: CredentialField): Promise<void> {
	await navigator.clipboard.writeText(field.value);
	copiedFieldId = field.id;
	window.setTimeout(() => {
		copiedFieldId = '';
	}, 1400);
}

function fieldLabel(field: CredentialField): string {
	return displayFieldLabel(credential.type, field, t().credentials);
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

<div class="grid grid-cols-[minmax(0,1fr)] gap-4">
	<div class="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
		<div class="grid min-w-0 gap-1">
			<Badge variant="secondary" class="w-fit">
				{templateName}
			</Badge>
			<h2 class="[overflow-wrap:anywhere] text-lg font-semibold leading-tight">{title}</h2>
		</div>
	</div>

	<div class="grid gap-2">
		{#each credential.fields as field (field.id)}
			<div class="flex min-w-0 flex-col gap-2 rounded-md border border-border bg-muted/20 p-3 sm:flex-row sm:items-center">
				<div class="min-w-0 [overflow-wrap:anywhere] text-sm font-medium text-muted-foreground sm:w-28 sm:shrink-0">
					{fieldLabel(field)}
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
							aria-label={revealedFields[field.id]
								? t().credentials.hideNamed(fieldLabel(field))
								: t().credentials.showNamed(fieldLabel(field))}
							title={revealedFields[field.id] ? t().credentials.hideValue : t().credentials.showValue}
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
						aria-label={t().credentials.copyNamed(fieldLabel(field))}
						title={t().credentials.copyNamed(fieldLabel(field))}
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
			<p class="text-sm font-medium text-muted-foreground">{t().credentials.notes}</p>
			<p class="whitespace-pre-wrap [overflow-wrap:anywhere] text-sm">{credential.notes}</p>
		</div>
	{/if}
</div>
