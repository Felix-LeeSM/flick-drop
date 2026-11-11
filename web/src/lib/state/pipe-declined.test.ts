import { describe, expect, it } from 'vitest';
import { rememberDeclinedCode, wasDeclinedCode } from './pipe-declined';

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
		await rememberDeclinedCode('741928', 180, storage, 1000);

		// Then: 창 안에서는 거절로 기억하고, 다른 코드는 무관하다
		expect(await wasDeclinedCode('741928', storage, 1000 + 179_000)).toBe(true);
		expect(await wasDeclinedCode('123456', storage, 1000)).toBe(false);

		// Then: 창이 닫히면 같은 숫자는 새 세션이므로 잊고 항목도 지운다
		expect(await wasDeclinedCode('741928', storage, 1000 + 180_000)).toBe(false);
		expect(storage.entries.size).toBe(0);
	});

	it('stores a hash, never the code itself', async () => {
		const storage = memoryStorage();
		await rememberDeclinedCode('741928', 180, storage, 0);

		const [key, value] = [...storage.entries][0];
		expect(key).not.toContain('741928');
		expect(value).not.toContain('741928');
		expect(key).toMatch(/^flick:pipe-declined:[0-9a-f]{64}$/);
	});
});
