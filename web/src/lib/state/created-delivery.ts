type Handoff = { id: string; token: string; recipientUrl: string; usesPassphrase: boolean };

// One transient handoff, consumed when the management page mounts. Never persist it.
let handoff: Handoff | null = null;

export function handOffCreatedDelivery(value: Handoff): void {
	handoff = value;
}

export function takeCreatedDelivery(id: string, token: string): Handoff | null {
	const value = handoff;
	handoff = null;
	return value?.id === id && value.token === token ? value : null;
}

export function clearCreatedDelivery(): void {
	handoff = null;
}
