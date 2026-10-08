<script lang="ts">
import { untrack } from 'svelte';
import { Button, buttonVariants } from '$lib/components/ui/button';
import {
	DEFAULT_TTL_SECONDS,
	formatTtlRange,
	MAX_TTL_SECONDS,
	MIN_TTL_SECONDS
} from '$lib/config/ttl';

type Unit = 'minutes' | 'hours' | 'days';
type Props = {
	seconds?: number;
	disabled?: boolean;
	label?: string;
	groupLabel?: string;
};

let {
	seconds = $bindable(DEFAULT_TTL_SECONDS),
	disabled = false,
	label = 'Expires after',
	groupLabel = 'Lifetime'
}: Props = $props();

const id = $props.id();
const unitFactor: Record<Unit, number> = { minutes: 60, hours: 3600, days: 86_400 };
const presets = [
	{ label: '10 min', value: 600 },
	{ label: '1 hour', value: 3600 },
	{ label: '24 hours', value: 86_400 },
	{ label: '7 days', value: 604_800 }
];

const initialSeconds = untrack(() => seconds);
const initialCustom = !presets.some((option) => option.value === initialSeconds);
const initialUnit: Unit =
	initialSeconds % unitFactor.days === 0
		? 'days'
		: initialSeconds % unitFactor.hours === 0
			? 'hours'
			: 'minutes';
let customActive = $state(initialCustom);
let customValue = $state(initialCustom ? String(initialSeconds / unitFactor[initialUnit]) : '2');
let customUnit = $state<Unit>(initialCustom ? initialUnit : 'days');
// Preserve exact configured seconds when the initial minutes display is fractional.
let customSeconds = $state(initialCustom ? initialSeconds : 2 * unitFactor.days);
const invalid = $derived(
	!Number.isInteger(seconds) || seconds < MIN_TTL_SECONDS || seconds > MAX_TTL_SECONDS
);

function selectCustom(): void {
	customActive = true;
	seconds = customSeconds;
}

function updateCustom(): void {
	const value = Number(customValue) * unitFactor[customUnit];
	customSeconds = Number.isSafeInteger(value) ? value : 0;
	selectCustom();
}
</script>

<div class="grid gap-2.5">
	<span class="text-sm font-medium">{label}</span>
	<div class="flex flex-wrap items-center gap-2" role="group" aria-label={groupLabel}>
		{#each presets as option (option.value)}
			<Button
				type="button"
				variant={!customActive && seconds === option.value ? 'toggleActive' : 'toggle'}
				size="pill"
				aria-pressed={!customActive && seconds === option.value}
				disabled={disabled || option.value < MIN_TTL_SECONDS || option.value > MAX_TTL_SECONDS}
				onclick={() => {
					customActive = false;
					seconds = option.value;
				}}
			>
				{option.label}
			</Button>
		{/each}
		<div
			class={`${buttonVariants({ variant: customActive ? 'toggleActive' : 'toggle', size: 'pill' })} px-1.5`}
			class:opacity-50={disabled}
		>
			<input
				type="text"
				inputmode="numeric"
				aria-label="Custom lifetime value"
				aria-invalid={invalid}
				aria-describedby={invalid ? `${id}-error` : undefined}
				placeholder="2"
				value={customValue}
				class="w-12 min-w-0 rounded-sm border-0 bg-transparent p-0 text-center font-mono text-xs leading-none outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background sm:text-sm"
				{disabled}
				onfocus={selectCustom}
				oninput={(event) => {
					customValue = event.currentTarget.value.replace(/\D/g, '');
					event.currentTarget.value = customValue;
					updateCustom();
				}}
			/>
			<select
				value={customUnit}
				aria-label="Custom lifetime unit"
				class="cursor-pointer appearance-none rounded-sm border-0 bg-transparent p-0 font-mono text-xs leading-none outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background sm:text-sm"
				{disabled}
				onchange={(event) => {
					customUnit = event.currentTarget.value as Unit;
					updateCustom();
				}}
			>
				<option value="minutes">min</option>
				<option value="hours">hours</option>
				<option value="days">days</option>
			</select>
		</div>
	</div>
	{#if invalid}
		<p id={`${id}-error`} class="text-sm text-destructive" role="alert" aria-live="assertive">
			{formatTtlRange(MIN_TTL_SECONDS, MAX_TTL_SECONDS)}
		</p>
	{/if}
</div>
