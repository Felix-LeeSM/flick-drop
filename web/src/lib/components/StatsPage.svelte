<script lang="ts">
import { KeyRoundIcon } from '@lucide/svelte';
import { onMount } from 'svelte';
import {
	createStatsApiClient,
	StatsApiError,
	type StatsDaily,
	type StatsReport,
	USAGE_METRICS
} from '#lib/api/stats.js';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';

// The admin token lives in sessionStorage only: it dies with the tab and never
// reaches localStorage, IndexedDB, or any URL. It is a view credential for
// aggregate counts, not a key or passphrase, but it still never leaves the
// device except in the Authorization header.
const TOKEN_STORAGE_KEY = 'flick-stats-token';
const api = createStatsApiClient();

let tokenInput = $state('');
let report = $state<StatsReport | null>(null);
let busy = $state(false);
let errorMessage = $state('');
let unlocked = $state(false);

onMount(() => {
	const saved = sessionStorage.getItem(TOKEN_STORAGE_KEY);
	if (saved) {
		tokenInput = saved;
		unlocked = true;
		void load();
	}
});

async function load(): Promise<void> {
	busy = true;
	errorMessage = '';
	try {
		report = await api.get(tokenInput);
	} catch (error) {
		report = null;
		errorMessage =
			error instanceof StatsApiError
				? error.message
				: 'Could not load the usage report. Try again.';
	} finally {
		busy = false;
	}
}

function submit(event: SubmitEvent): void {
	event.preventDefault();
	const token = tokenInput.trim();
	if (!token) {
		errorMessage = 'Enter the stats token first.';
		return;
	}
	tokenInput = token;
	sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
	unlocked = true;
	void load();
}

function forget(): void {
	sessionStorage.removeItem(TOKEN_STORAGE_KEY);
	tokenInput = '';
	report = null;
	unlocked = false;
	errorMessage = '';
}

function createdOn(day: StatsDaily): number {
	return (
		(day.metrics[USAGE_METRICS.createdText] ?? 0) + (day.metrics[USAGE_METRICS.createdFile] ?? 0)
	);
}

const createdMax = $derived(
	report ? Math.max(...report.series.map((day) => createdOn(day)), 1) : 1
);
const createdRange = $derived.by(() => {
	if (!report || report.series.length === 0) {
		return '';
	}
	return `${report.series[0].day} – ${report.series[report.series.length - 1].day}`;
});
const totalsCreated = $derived(
	report
		? (report.totals[USAGE_METRICS.createdText] ?? 0) +
				(report.totals[USAGE_METRICS.createdFile] ?? 0)
		: 0
);

function formatCount(value: number | undefined): string {
	return (value ?? 0).toLocaleString('en-US');
}

function formatBytes(value: number | undefined): string {
	let bytes = value ?? 0;
	if (bytes < 1024) {
		return `${bytes} B`;
	}
	const units = ['KiB', 'MiB', 'GiB'];
	let unit = -1;
	do {
		bytes /= 1024;
		unit += 1;
	} while (bytes >= 1024 && unit < units.length - 1);
	return `${bytes >= 10 ? Math.round(bytes) : bytes.toFixed(1)} ${units[unit]}`;
}

function dayLabel(day: string): string {
	const date = new Date(`${day}T00:00:00Z`);
	return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

function weekdayLabel(day: string): string {
	const date = new Date(`${day}T00:00:00Z`);
	return date.toLocaleDateString('en-US', { weekday: 'short', timeZone: 'UTC' });
}
</script>

<DeliveryFrame>
	<section class="grid gap-7">
		<header class="grid gap-1">
			<p class="micro text-muted-foreground">Admin · usage</p>
			<h1 class="page-title">Who has been sending?</h1>
			<p class="text-sm text-muted-foreground">
				Daily aggregate counts recorded with each delivery event. The ephemeral
				deliveries themselves are deleted on expiry — only these counts remain.
			</p>
		</header>

		{#if !unlocked}
			<form class="grid gap-3" onsubmit={submit}>
				<div class="grid gap-1.5">
					<label class="micro text-muted-foreground" for="stats-token">Stats token</label>
					<div class="relative">
						<KeyRoundIcon
							class="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
							aria-hidden="true"
						/>
						<input
							id="stats-token"
							class="h-12 w-full rounded-md border border-input bg-card pr-3 pl-9 text-sm placeholder:text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring"
							type="password"
							autocomplete="off"
							spellcheck="false"
							placeholder="FLICK_STATS_TOKEN"
							bind:value={tokenInput}
						/>
					</div>
				</div>
				{#if errorMessage}
					<p class="text-sm text-destructive" role="alert">{errorMessage}</p>
				{/if}
				<button
					class="inline-flex h-11 items-center justify-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50"
					type="submit"
				>
					Show usage
				</button>
				<p class="text-xs text-muted-foreground">
					The token stays in this tab's sessionStorage and is forgotten when the tab closes.
				</p>
			</form>
		{:else}
			<div class="flex items-center justify-between">
				<p class="micro text-muted-foreground">Last {report?.days ?? 30} days{report ? ` · ${createdRange}` : ''}</p>
				<button
					class="inline-flex h-9 items-center rounded-sm text-xs font-medium text-muted-foreground underline decoration-primary decoration-2 underline-offset-[6px] focus-visible:outline-2"
					type="button"
					onclick={forget}
				>
					Forget token
				</button>
			</div>

			{#if errorMessage}
				<p class="text-sm text-destructive" role="alert">{errorMessage}</p>
			{/if}

			{#if report}
				<section class="grid gap-3" aria-label="Daily deliveries created">
					<div
						class="flex h-36 items-end gap-[2px]"
						role="img"
						aria-label="Bar chart of deliveries created per day, {createdRange}"
					>
						{#each report.series as day (day.day)}
							{@const created = createdOn(day)}
							{@const height = Math.round((created / createdMax) * 100)}
							<div class="group relative flex h-full min-w-0 flex-1 items-end">
								<div
									class="w-full rounded-t-[4px] bg-chart-1 transition-[height] dark:bg-[color-mix(in_oklch,var(--color-primary)_78%,black)] {created === 0 ? 'bg-muted' : ''}"
									style="height: {created === 0 ? 2 : height}%"
								></div>
								<div
									class="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 rounded-md border border-border bg-popover px-2 py-1 text-xs whitespace-nowrap text-popover-foreground group-hover:block"
								>
									<span class="font-medium">{dayLabel(day.day)}</span>
									<span class="text-muted-foreground"> · {created} created</span>
								</div>
							</div>
						{/each}
					</div>
					<p class="micro text-muted-foreground">Deliveries created per day · peak {formatCount(createdMax)}</p>
				</section>

				<section class="grid grid-cols-2 gap-3 sm:grid-cols-4" aria-label="Totals">
					{#each [
						{ label: 'Created', value: formatCount(totalsCreated), hint: 'all time' },
						{ label: 'Opened', value: formatCount(report.totals[USAGE_METRICS.opened]), hint: 'all time' },
						{ label: 'Uploaded', value: formatBytes(report.totals[USAGE_METRICS.bytesUploaded]), hint: 'encrypted bytes' },
						{ label: 'Active now', value: formatCount(report.live.active_secrets), hint: `${formatCount(report.live.live_requests)} request links live` }
					] as tile (tile.label)}
						<div class="grid gap-1 rounded-lg border border-border bg-card p-3">
							<p class="micro text-muted-foreground">{tile.label}</p>
							<p class="font-serif text-2xl leading-none">{tile.value}</p>
							<p class="text-xs text-muted-foreground">{tile.hint}</p>
						</div>
					{/each}
				</section>

				<section class="grid grid-cols-2 gap-3 sm:grid-cols-4" aria-label="Delivery totals">
					{#each [
						{ label: 'Open failed', value: formatCount(report.totals[USAGE_METRICS.openFailed]) },
						{ label: 'Requests made', value: formatCount(report.totals[USAGE_METRICS.requestCreated]) },
						{ label: 'Requests filled', value: formatCount(report.totals[USAGE_METRICS.requestSubmitted]) },
						{ label: 'Requests opened', value: formatCount(report.totals[USAGE_METRICS.requestConsumed]) }
					] as tile (tile.label)}
						<div class="grid gap-1 rounded-lg border border-border bg-card p-3">
							<p class="micro text-muted-foreground">{tile.label}</p>
							<p class="font-serif text-2xl leading-none">{tile.value}</p>
						</div>
					{/each}
				</section>

				<details class="rounded-lg border border-border bg-card">
					<summary class="cursor-pointer px-3 py-2 text-sm font-medium select-none">
						View the daily numbers as a table
					</summary>
					<div class="max-h-72 overflow-auto px-3 pb-3">
						<table class="w-full text-left text-xs">
							<caption class="sr-only">Daily usage counters for the last {report.days} days</caption>
							<thead class="text-muted-foreground">
								<tr>
									<th scope="col" class="py-1 pr-2 font-medium">Day</th>
									<th scope="col" class="py-1 pr-2 font-medium">Created</th>
									<th scope="col" class="py-1 pr-2 font-medium">Opened</th>
									<th scope="col" class="py-1 pr-2 font-medium">Failed</th>
									<th scope="col" class="py-1 pr-2 font-medium">Requests</th>
									<th scope="col" class="py-1 font-medium">Bytes</th>
								</tr>
							</thead>
							<tbody>
								{#each [...report.series].reverse() as day (day.day)}
									{@const reqs =
										(day.metrics[USAGE_METRICS.requestSubmitted] ?? 0) +
										(day.metrics[USAGE_METRICS.requestConsumed] ?? 0)}
									<tr class="border-t border-border">
										<td class="py-1 pr-2">{day.day}</td>
										<td class="py-1 pr-2">{formatCount(createdOn(day))}</td>
										<td class="py-1 pr-2">{formatCount(day.metrics[USAGE_METRICS.opened])}</td>
										<td class="py-1 pr-2">{formatCount(day.metrics[USAGE_METRICS.openFailed])}</td>
										<td class="py-1 pr-2">{formatCount(reqs)}</td>
										<td class="py-1">{formatBytes(day.metrics[USAGE_METRICS.bytesUploaded])}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</details>
			{/if}
			{#if busy}
				<p class="micro text-muted-foreground" aria-live="polite">Loading the report…</p>
			{/if}
		{/if}
	</section>
</DeliveryFrame>
