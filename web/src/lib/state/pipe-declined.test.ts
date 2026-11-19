import { describe, expect, it } from 'vitest';
import { rememberDeclinedCode, sessionExpiresAt, wasDeclinedCode } from './pipe-declined';

function memoryStorage(): Storage & { entries: Map<string, string> } {
	const entries = new Map<string, string>();
	return {
		entries,
		get length() {
			return entries.size;
		},
		clear: () => entries.clear(),
		getItem: (key) => entries.get(key) ?? null,
		key: (index) => [...entries.keys()][index] ?? null,
		removeItem: (key) => {
			entries.delete(key);
		},
		setItem: (key, value) => {
			entries.set(key, value);
		}
	};
}

describe('pipe declined-code memory', () => {
	it('remembers a declined code until its session window closes', async () => {
		const storage = memoryStorage();
		await rememberDeclinedCode('741928', sessionExpiresAt(180, 1000), storage);

		expect(await wasDeclinedCode('741928', storage, 1000 + 179_000)).toBe(true);
		expect(await wasDeclinedCode('123456', storage, 1000)).toBe(false);

		expect(await wasDeclinedCode('741928', storage, 1000 + 180_000)).toBe(false);
		expect(storage.entries.size).toBe(0);
	});

	it('lapses when the session window closes, however late the decline came', async () => {
		const storage = memoryStorage();
		const expiresAt = sessionExpiresAt(180, 1000);
		await rememberDeclinedCode('741928', expiresAt, storage);

		expect(await wasDeclinedCode('741928', storage, 1000 + 179_000)).toBe(true);
		expect(await wasDeclinedCode('741928', storage, 1000 + 180_000)).toBe(false);
	});

	it('stores a hash, never the code itself', async () => {
		const storage = memoryStorage();
		await rememberDeclinedCode('741928', sessionExpiresAt(180, 0), storage);

		const [key, value] = [...storage.entries][0];
		expect(key).not.toContain('741928');
		expect(value).not.toContain('741928');
		expect(key).toMatch(/^flick:pipe-declined:[0-9a-f]{64}$/);
	});
});
