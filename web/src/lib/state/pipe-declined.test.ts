import { describe, expect, it } from 'vitest';
import { rememberDeclinedCode, sessionExpiresAt, wasDeclinedCode } from './pipe-declined';

// sessionStorage 대역: Storage 인터페이스 중 쓰는 부분만 Map으로 흉내 낸다.
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
		// Given: 3분(180초) 남은 세션에서 거절된 코드
		const storage = memoryStorage();
		await rememberDeclinedCode('741928', sessionExpiresAt(180, 1000), storage);

		// Then: 창 안에서는 거절로 기억하고, 다른 코드는 무관하다
		expect(await wasDeclinedCode('741928', storage, 1000 + 179_000)).toBe(true);
		expect(await wasDeclinedCode('123456', storage, 1000)).toBe(false);

		// Then: 창이 닫히면 같은 숫자는 새 세션이므로 잊고 항목도 지운다
		expect(await wasDeclinedCode('741928', storage, 1000 + 180_000)).toBe(false);
		expect(storage.entries.size).toBe(0);
	});

	it('lapses when the session window closes, however late the decline came', async () => {
		// Given: 참가 시각 1000에 180초가 남았고, 거절은 170초 뒤에 왔다
		const storage = memoryStorage();
		const expiresAt = sessionExpiresAt(180, 1000);
		await rememberDeclinedCode('741928', expiresAt, storage);

		// Then: 기억은 참가 시각 기준 창(1000 + 180초)에서 끝나고, 거절 시각에 180초를 더하지 않는다
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
