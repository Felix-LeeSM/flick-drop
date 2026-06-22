<script lang="ts">
import { EyeIcon, EyeOffIcon, PlusIcon, Trash2Icon } from '@lucide/svelte';
import type { HTMLInputAttributes, HTMLInputTypeAttribute } from 'svelte/elements';
import { Button } from '$lib/components/ui/button';
import { Input } from '$lib/components/ui/input';
import { Label } from '$lib/components/ui/label';
import { Textarea } from '$lib/components/ui/textarea';
import {
	addCustomField,
	type CredentialEnvelope,
	type CredentialField,
	type CredentialInputKind,
	getCredentialTemplate,
	removeField,
	setFieldValue
} from '$lib/credentials';

type Props = {
	envelope: CredentialEnvelope;
	disabled?: boolean;
};

type AutocompleteValue = HTMLInputAttributes['autocomplete'];
type FieldInputType = Exclude<HTMLInputTypeAttribute, 'file'>;

let { envelope = $bindable(), disabled = false }: Props = $props();

let revealedFields = $state<Record<string, boolean>>({});

const template = $derived(getCredentialTemplate(envelope.type));

function textInputValue(event: Event): string {
	return (event.currentTarget as HTMLInputElement | HTMLTextAreaElement).value;
}

function checkedInputValue(event: Event): boolean {
	return (event.currentTarget as HTMLInputElement).checked;
}

function updateTitle(event: Event): void {
	envelope = {
		...envelope,
		title: textInputValue(event)
	};
}

function updateNotes(event: Event): void {
	envelope = {
		...envelope,
		notes: textInputValue(event)
	};
}

function updateFieldLabel(fieldId: string, value: string): void {
	envelope = {
		...envelope,
		fields: envelope.fields.map((field) =>
			field.id === fieldId
				? {
						...field,
						label: value
					}
				: field
		)
	};
}

function updateFieldSecret(fieldId: string, secret: boolean): void {
	envelope = {
		...envelope,
		fields: envelope.fields.map((field) =>
			field.id === fieldId
				? {
						...field,
						secret
					}
				: field
		)
	};
}

function updateFieldValue(fieldId: string, value: string): void {
	envelope = setFieldValue(envelope, fieldId, value);
}

function addField(): void {
	envelope = addCustomField(envelope);
}

function removeCustomField(fieldId: string): void {
	envelope = removeField(envelope, fieldId);
}

function toggleReveal(fieldId: string): void {
	revealedFields = {
		...revealedFields,
		[fieldId]: !revealedFields[fieldId]
	};
}

function inputTypeFor(field: CredentialField, input: CredentialInputKind): FieldInputType {
	if (!field.secret) {
		return input === 'textarea' ? 'text' : input;
	}
	return revealedFields[field.id] ? (input === 'password' ? 'text' : input) : 'password';
}

function fieldInputKind(field: CredentialField): CredentialInputKind {
	const definition = template.fields.find((item) => item.id === field.id);
	return definition?.input ?? 'text';
}

function fieldPlaceholder(field: CredentialField): string | undefined {
	return template.fields.find((item) => item.id === field.id)?.placeholder;
}

function fieldAutocomplete(field: CredentialField): AutocompleteValue {
	return (template.fields.find((item) => item.id === field.id)?.autocomplete ??
		'off') as AutocompleteValue;
}
</script>

<div class="grid gap-4">
	<div class="grid gap-2">
		<Label for="credential-title">Title</Label>
		<Input
			id="credential-title"
			type="text"
			class="text-base md:text-sm"
			autocomplete="off"
			placeholder={template.titlePlaceholder}
			value={envelope.title ?? ''}
			disabled={disabled}
			oninput={updateTitle}
		/>
	</div>

	<div class="grid gap-3">
		{#each envelope.fields as field (field.id)}
			{@const inputKind = fieldInputKind(field)}
			{@const valueId = `credential-${envelope.type}-${field.id}-value`}
			<div class="grid gap-2 rounded-md border border-border bg-muted/20 p-3">
				<div class="flex min-w-0 items-start justify-between gap-2">
					{#if template.allowCustomFields}
						<div class="grid min-w-0 flex-1 gap-1">
							<Label for={`credential-${envelope.type}-${field.id}-label`}>Label</Label>
							<Input
								id={`credential-${envelope.type}-${field.id}-label`}
								type="text"
								class="h-9 text-base md:text-sm"
								autocomplete="off"
								value={field.label}
								disabled={disabled}
								oninput={(event) => {
									updateFieldLabel(field.id, textInputValue(event));
								}}
							/>
						</div>
						<Button
							type="button"
							variant="ghost"
							size="icon"
							class="mt-6 size-9 shrink-0"
							aria-label={`Remove ${field.label || 'field'}`}
							title="Remove field"
							disabled={disabled}
							onclick={() => {
								removeCustomField(field.id);
							}}
						>
							<Trash2Icon class="size-4" />
						</Button>
					{:else}
						<Label class="pt-1" for={valueId}>{field.label}</Label>
					{/if}
				</div>

				<div class="grid gap-2">
					{#if inputKind === 'textarea'}
						<Textarea
							id={valueId}
							class="min-h-24 resize-y text-base md:text-sm"
							autocomplete={fieldAutocomplete(field)}
							placeholder={fieldPlaceholder(field)}
							value={field.value}
							disabled={disabled}
							oninput={(event) => {
								updateFieldValue(field.id, textInputValue(event));
							}}
						/>
					{:else}
						<div class="relative">
							<Input
								id={valueId}
								type={inputTypeFor(field, inputKind)}
								class={field.secret ? 'h-10 pr-11 text-base md:text-sm' : 'h-10 text-base md:text-sm'}
								autocomplete={fieldAutocomplete(field)}
								placeholder={fieldPlaceholder(field)}
								value={field.value}
								disabled={disabled}
								autocapitalize="none"
								spellcheck="false"
								data-1p-ignore={field.secret ? 'true' : undefined}
								data-bwignore={field.secret ? 'true' : undefined}
								data-lpignore={field.secret ? 'true' : undefined}
								oninput={(event) => {
									updateFieldValue(field.id, textInputValue(event));
								}}
							/>
							{#if field.secret}
								<Button
									type="button"
									variant="ghost"
									size="icon"
									class="absolute right-0 top-0 size-10"
									aria-label={revealedFields[field.id] ? `Hide ${field.label}` : `Show ${field.label}`}
									title={revealedFields[field.id] ? 'Hide value' : 'Show value'}
									disabled={disabled}
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
						</div>
					{/if}

					{#if template.allowCustomFields}
						<label class="flex min-h-9 items-center gap-2 text-sm text-muted-foreground">
							<input
								type="checkbox"
								class="size-4 rounded border-border"
								checked={field.secret}
								disabled={disabled}
								onchange={(event) => {
									updateFieldSecret(field.id, checkedInputValue(event));
								}}
							/>
							<span>Secret</span>
						</label>
					{/if}
				</div>
			</div>
		{/each}
	</div>

	{#if template.allowCustomFields}
		<Button
			type="button"
			variant="outline"
			class="h-10 w-full"
			disabled={disabled}
			onclick={addField}
		>
			<PlusIcon class="size-4" aria-hidden="true" />
			Add field
		</Button>
	{/if}

	<div class="grid gap-2">
		<Label for="credential-notes">Notes</Label>
		<Textarea
			id="credential-notes"
			class="min-h-24 resize-y text-base md:text-sm"
			autocomplete="off"
			placeholder="Optional"
			value={envelope.notes ?? ''}
			disabled={disabled}
			oninput={updateNotes}
		/>
	</div>
</div>
