<script lang="ts">
import { ChevronDownIcon, UsersIcon, XIcon } from '@lucide/svelte';
import { tick } from 'svelte';
import { Button } from '#lib/components/ui/button/index.js';
import VerifyDigits from '#lib/components/VerifyDigits.svelte';
import type { PendingReceiver } from '#lib/state/pipe-relay.svelte.js';
import { cn } from '#lib/utils.js';

// The sender's waiting room: every device that entered the 6-digit code, in
// join order, each with its own server-drawn 3-digit number. The sender sends
// to the row whose number the receiver reads out, or denies single rows.
type Props = {
	pending: PendingReceiver[];
	/** Server room size (internal/piperelay/coordinator.go MaxPendingConnections). */
	capacity: number;
	/** Local clock, ticking once a second, for the relative join time. */
	now: number;
	onSend: (connectionId: string) => void;
	onDeny: (connectionId: string) => void;
};

let { pending, capacity, now, onSend, onDeny }: Props = $props();

// The first rows stay in view; the rest fold behind one disclosure button so
// ten waiting devices do not push the 6-digit code off screen.
const VISIBLE_ROWS = 3;
let expanded = $state(false);
const hiddenCount = $derived(Math.max(0, pending.length - VISIBLE_ROWS));
const shown = $derived(expanded ? pending : pending.slice(0, VISIBLE_ROWS));

let heading: HTMLHeadingElement | undefined = $state();
let rows: HTMLUListElement | undefined = $state();

// Denying removes the focused row, so focus moves to the Send button of the row
// that takes its place, or to the list heading when none is left, instead of
// falling back to the page body.
async function deny(connectionId: string): Promise<void> {
	const position = pending.findIndex((receiver) => receiver.connectionId === connectionId);
	const nextId = pending[position + 1]?.connectionId;
	onDeny(connectionId);
	await tick();
	const nextSend =
		nextId === undefined
			? null
			: rows?.querySelector<HTMLButtonElement>(`[data-send-for="${CSS.escape(nextId)}"]`);
	(nextSend ?? heading)?.focus();
}

function ago(joinedAt: number): string {
	const seconds = Math.max(0, Math.floor((now - joinedAt) / 1000));
	if (seconds < 5) {
		return 'just now';
	}
	return seconds < 60 ? `${seconds}s ago` : `${Math.floor(seconds / 60)}m ago`;
}
</script>

<section class="grid gap-3 text-left" aria-labelledby="pipe-pending-heading">
	<div class="grid gap-1 text-center">
		<span
			class="inline-flex items-center justify-center gap-1.5 justify-self-center rounded-full bg-primary/15 px-2.5 py-0.5 text-[11px] font-bold text-primary"
		>
			<UsersIcon class="size-3.5" aria-hidden="true" />
			<!-- Announced politely as devices join or leave. -->
			<span aria-live="polite" aria-atomic="true">{pending.length} of {capacity} devices waiting</span>
		</span>
		<h3
			id="pipe-pending-heading"
			bind:this={heading}
			tabindex="-1"
			class="text-base font-semibold text-foreground outline-none"
		>
			Which number does your receiver see?
		</h3>
		<p class="text-xs text-muted-foreground">
			{pending.length === 0
				? 'Each device that enters the code appears here with its own 3-digit number.'
				: 'Ask your receiver to read their number, then send to that row. The other devices get nothing.'}
		</p>
	</div>

	{#if pending.length > 0}
		<ul id="pipe-pending-rows" bind:this={rows} class="grid gap-2" aria-label="Waiting devices">
			{#each shown as receiver (receiver.connectionId)}
				<li class="flex items-center gap-2.5 rounded-xl border border-border bg-background p-2.5 shadow-xs">
					<VerifyDigits
						code={receiver.verifyCode}
						size="sm"
						label="Number {receiver.verifyCode.split('').join(' ')}"
					/>
					<div class="grid min-w-0 flex-1">
						<p class="truncate text-sm font-semibold text-foreground">Connection {receiver.index}</p>
						<!-- The device label truncates on its own so the time never gets cut off. -->
						<p class="flex min-w-0 gap-1 text-[11px] text-muted-foreground">
							<span class="truncate">{receiver.device}</span>
							<span class="shrink-0" aria-hidden="true">·</span>
							<span class="shrink-0 tabular-nums">{ago(receiver.joinedAt)}</span>
						</p>
					</div>
					<Button
						type="button"
						variant="outline"
						size="icon-lg"
						class="size-11 shrink-0 border-destructive/40 text-destructive hover:bg-destructive/10 hover:text-destructive"
						aria-label="Deny connection {receiver.index}"
						onclick={() => deny(receiver.connectionId)}
					>
						<XIcon class="size-4" aria-hidden="true" />
					</Button>
					<Button
						type="button"
						class="h-11 shrink-0 px-4 font-bold"
						data-send-for={receiver.connectionId}
						aria-label="Send to connection {receiver.index}, number {receiver.verifyCode}"
						onclick={() => onSend(receiver.connectionId)}
					>
						Send
					</Button>
				</li>
			{/each}
		</ul>

		{#if hiddenCount > 0}
			<Button
				type="button"
				variant="ghost"
				size="sm"
				class="h-11 justify-self-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
				aria-expanded={expanded}
				aria-controls="pipe-pending-rows"
				onclick={() => {
					expanded = !expanded;
				}}
			>
				<ChevronDownIcon
					class={cn('size-4 transition-transform motion-reduce:transition-none', expanded && 'rotate-180')}
					aria-hidden="true"
				/>
				{expanded ? 'Show fewer' : `Show ${hiddenCount} more`}
			</Button>
		{/if}
	{/if}
</section>
