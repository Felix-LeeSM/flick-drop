import { strFromU8, unzipSync } from 'fflate';
import { describe, expect, it } from 'vitest';
import { BUNDLE_FILENAME, bundleFiles, type PickedFile } from './bundle';

function file(name: string, body: string, type = 'text/plain'): File {
	return new File([body], name, { type });
}

// A loose file: its path is its own name.
function loose(name: string, body: string): PickedFile {
	return { file: file(name, body), path: name };
}

// A file inside a picked folder: the path keeps the folder structure.
function inFolder(path: string, body: string): PickedFile {
	return { file: file(path.split('/').at(-1) ?? path, body), path };
}

async function unzip(bundle: File): Promise<Record<string, string>> {
	const entries = unzipSync(new Uint8Array(await bundle.arrayBuffer()));
	return Object.fromEntries(
		Object.entries(entries).map(([name, bytes]) => [name, strFromU8(bytes)])
	);
}

describe('bundleFiles', () => {
	it('passes a single file through unchanged', async () => {
		const only = loose('report.txt', 'hello');
		const result = await bundleFiles([only]);
		expect(result).toBe(only.file);
	});

	it('zips multiple files into bundle.zip, preserving contents', async () => {
		const result = await bundleFiles([loose('a.txt', 'alpha'), loose('b.txt', 'beta')]);
		expect(result.name).toBe(BUNDLE_FILENAME);
		expect(result.type).toBe('application/zip');
		expect(await unzip(result)).toEqual({ 'a.txt': 'alpha', 'b.txt': 'beta' });
	});

	it('disambiguates duplicate names so no file is lost', async () => {
		const result = await bundleFiles([loose('dup.txt', 'first'), loose('dup.txt', 'second')]);
		expect(await unzip(result)).toEqual({ 'dup.txt': 'first', 'dup (1).txt': 'second' });
	});

	it('streams files larger than one read chunk without losing bytes', async () => {
		const big = Uint8Array.from({ length: 300_000 }, (_, i) => (i * 7) % 256);
		const result = await bundleFiles([
			{ file: new File([big], 'big.bin'), path: 'big.bin' },
			loose('small.txt', 'tail')
		]);
		const entries = unzipSync(new Uint8Array(await result.arrayBuffer()));
		expect(entries['big.bin']).toEqual(big);
		expect(strFromU8(entries['small.txt'])).toBe('tail');
	});

	it('uses folder-relative paths as zip entry names and names a lone folder after itself', async () => {
		const result = await bundleFiles([
			inFolder('photos/a.jpg', 'top'),
			inFolder('photos/2024/b.jpg', 'nested')
		]);
		expect(result.name).toBe('photos.zip');
		expect(await unzip(result)).toEqual({ 'photos/a.jpg': 'top', 'photos/2024/b.jpg': 'nested' });
	});

	it('zips a folder holding a single file so its structure survives', async () => {
		const result = await bundleFiles([inFolder('notes/todo.txt', 'one')]);
		expect(result.name).toBe('notes.zip');
		expect(await unzip(result)).toEqual({ 'notes/todo.txt': 'one' });
	});

	it('falls back to bundle.zip for a folder mixed with loose files or other folders', async () => {
		const withLoose = await bundleFiles([inFolder('photos/a.jpg', 'a'), loose('readme.txt', 'r')]);
		expect(withLoose.name).toBe(BUNDLE_FILENAME);
		expect(await unzip(withLoose)).toEqual({ 'photos/a.jpg': 'a', 'readme.txt': 'r' });

		const twoFolders = await bundleFiles([
			inFolder('photos/a.jpg', 'a'),
			inFolder('docs/b.txt', 'b')
		]);
		expect(twoFolders.name).toBe(BUNDLE_FILENAME);
	});

	it('disambiguates a repeated path inside its folder, ignoring dots in folder names', async () => {
		const result = await bundleFiles([
			inFolder('v1.2/same.txt', 'first'),
			inFolder('v1.2/same.txt', 'second'),
			inFolder('v1.2/README', 'third'),
			inFolder('v1.2/README', 'fourth'),
			inFolder('v1.2/other/same.txt', 'elsewhere')
		]);
		expect(await unzip(result)).toEqual({
			'v1.2/same.txt': 'first',
			'v1.2/same (1).txt': 'second',
			'v1.2/README': 'third',
			'v1.2/README (1)': 'fourth',
			'v1.2/other/same.txt': 'elsewhere'
		});
	});

	it('rejects an empty batch', async () => {
		await expect(bundleFiles([])).rejects.toThrow();
	});
});
