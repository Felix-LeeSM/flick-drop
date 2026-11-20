// Per-tab memory of a sender's decline, sparing /api/pipe/join; the server blocks anyway
// (internal/piperelay/coordinator.go Coordinator.Decline). Stores only a SHA-256 of the code;
// codes are reused by later sessions, so each entry lapses with its session.

const KEY_PREFIX = 'flick:pipe-declined:';

async function codeKey(normalizedCode: string): Promise<string> {
	const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(normalizedCode));
	const hex = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0'));
	return KEY_PREFIX + hex.join('');
}

/** Compute when the join response arrives: the window counts from then, not from the decline. */
export function sessionExpiresAt(expiresInSeconds: number, joinedAt: number = Date.now()): number {
	return joinedAt + Math.max(0, expiresInSeconds) * 1000;
}

export async function rememberDeclinedCode(
	normalizedCode: string,
	expiresAt: number,
	storage: Storage = sessionStorage
): Promise<void> {
	storage.setItem(await codeKey(normalizedCode), String(expiresAt));
}

export async function wasDeclinedCode(
	normalizedCode: string,
	storage: Storage = sessionStorage,
	now: number = Date.now()
): Promise<boolean> {
	const key = await codeKey(normalizedCode);
	const expiresAt = Number(storage.getItem(key));
	if (!Number.isFinite(expiresAt) || expiresAt <= now) {
		storage.removeItem(key);
		return false;
	}
	return true;
}
