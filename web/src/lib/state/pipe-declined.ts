// Remembers, per browser tab, that the sender declined this device for a live
// relay code, so a reload or a re-typed code shows "declined" without even
// calling /api/pipe/join. The server blocks the device anyway
// (internal/piperelay/coordinator.go Coordinator.Decline); this only spares the
// request.
//
// sessionStorage holds a SHA-256 of the 6-digit code and the session's expiry
// time, never the code itself or the salt, so the entry cannot rebuild the
// pipe passphrase. A code is reused by a later, unrelated session once the
// first one ends, so each entry lapses with the session that declined it.

const KEY_PREFIX = 'flick:pipe-declined:';

async function codeKey(normalizedCode: string): Promise<string> {
	const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(normalizedCode));
	const hex = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0'));
	return KEY_PREFIX + hex.join('');
}

/**
 * The local clock time (ms) the session's rendezvous window closes, from the
 * `expires_in` that `/api/pipe/join` returned. Compute it when the join
 * response arrives: the window counts from then, not from the later decline.
 */
export function sessionExpiresAt(expiresInSeconds: number, joinedAt: number = Date.now()): number {
	return joinedAt + Math.max(0, expiresInSeconds) * 1000;
}

/** Records the decline until `expiresAt` (from `sessionExpiresAt`), when the declining session's window closes. */
export async function rememberDeclinedCode(
	normalizedCode: string,
	expiresAt: number,
	storage: Storage = sessionStorage
): Promise<void> {
	storage.setItem(await codeKey(normalizedCode), String(expiresAt));
}

/** True while a decline for this code is still within its session's window. */
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
