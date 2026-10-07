import { strFromU8, unzipSync } from 'fflate';
import { describe, expect, it } from 'vitest';
import { BUNDLE_FILENAME, bundleFiles } from './bundle';

function file(name: string, body: string, type = 'text/plain'): File {
	return new File([body], name, { type });
}

async function unzip(bundle: File): Promise<Record<string, string>> {
	const entries = unzipSync(new Uint8Array(await bundle.arrayBuffer()));
	return Object.fromEntries(
		Object.entries(entries).map(([name, bytes]) => [name, strFromU8(bytes)])
	);
}

describe('bundleFiles', () => {
	it('passes a single file through unchanged', async () => {
		const only = file('report.txt', 'hello');
		const result = await bundleFiles([only]);
		expect(result).toBe(only);
	});

	it('zips multiple files into bundle.zip, preserving contents', async () => {
		const result = await bundleFiles([file('a.txt', 'alpha'), file('b.txt', 'beta')]);
		expect(result.name).toBe(BUNDLE_FILENAME);
		expect(result.type).toBe('application/zip');
		expect(await unzip(result)).toEqual({ 'a.txt': 'alpha', 'b.txt': 'beta' });
	});

	it('disambiguates duplicate names so no file is lost', async () => {
		const result = await bundleFiles([file('dup.txt', 'first'), file('dup.txt', 'second')]);
		expect(await unzip(result)).toEqual({ 'dup.txt': 'first', 'dup (1).txt': 'second' });
	});

	it('rejects an empty batch', async () => {
		await expect(bundleFiles([])).rejects.toThrow();
	});
});
