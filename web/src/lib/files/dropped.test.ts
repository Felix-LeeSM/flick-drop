import { describe, expect, it } from 'vitest';
import { readDirectory, readDrop } from './dropped';

type FakeNode = FakeFile | FakeDirectory;
// failAfter: 그만큼 묶음을 준 뒤 readEntries가 에러 콜백을 부른다.
type FakeFile = { name: string; body: string; unreadable?: boolean };
type FakeDirectory = { name: string; batches: FakeNode[][]; failAfter?: number };

function fakeEntry(node: FakeNode): FileSystemEntry {
	if ('body' in node) {
		return {
			name: node.name,
			isFile: true,
			isDirectory: false,
			file: (resolve: (file: File) => void, reject: (error: DOMException) => void) =>
				node.unreadable
					? reject(new DOMException('gone', 'NotFoundError'))
					: resolve(new File([node.body], node.name))
		} as unknown as FileSystemFileEntry;
	}
	return {
		name: node.name,
		isFile: false,
		isDirectory: true,
		createReader: () => {
			const pending = [...node.batches, []];
			let served = 0;
			return {
				readEntries: (
					resolve: (entries: FileSystemEntry[]) => void,
					reject: (error: DOMException) => void
				) => {
					if (served === node.failAfter) {
						reject(new DOMException('denied', 'NotReadableError'));
						return;
					}
					served += 1;
					resolve((pending.shift() ?? []).map(fakeEntry));
				}
			};
		}
	} as unknown as FileSystemDirectoryEntry;
}

function fakeItems(nodes: FakeNode[]): DataTransferItemList {
	const items = nodes.map((node) => ({
		kind: 'file',
		webkitGetAsEntry: () => fakeEntry(node),
		getAsFile: () => ('body' in node ? new File([node.body], node.name) : null)
	}));
	return items as unknown as DataTransferItemList;
}

async function summarize(files: { file: File; path: string }[]): Promise<Record<string, string>> {
	const pairs = await Promise.all(files.map(async ({ file, path }) => [path, await file.text()]));
	return Object.fromEntries(pairs);
}

describe('readDirectory', () => {
	it('reads every readEntries batch and nested folder with relative paths', async () => {
		const photos: FakeDirectory = {
			name: 'photos',
			batches: [
				[{ name: 'a.jpg', body: 'A' }],
				[
					{ name: 'b.jpg', body: 'B' },
					{ name: '2024', batches: [[{ name: 'c.jpg', body: 'C' }]] }
				]
			]
		};

		const { files, skipped } = await readDirectory(
			fakeEntry(photos) as FileSystemDirectoryEntry,
			''
		);

		expect(await summarize(files)).toEqual({
			'photos/a.jpg': 'A',
			'photos/b.jpg': 'B',
			'photos/2024/c.jpg': 'C'
		});
		expect(skipped).toBe(0);
	});

	it('skips empty subfolders', async () => {
		const root: FakeDirectory = {
			name: 'root',
			batches: [
				[
					{ name: 'empty', batches: [] },
					{ name: 'keep.txt', body: 'k' }
				]
			]
		};

		const { files } = await readDirectory(fakeEntry(root) as FileSystemDirectoryEntry, '');

		expect(await summarize(files)).toEqual({ 'root/keep.txt': 'k' });
	});

	it('skips an unreadable file and a failing listing while keeping what was read', async () => {
		const root: FakeDirectory = {
			name: 'root',
			batches: [
				[
					{ name: 'ok.txt', body: 'o' },
					{ name: 'broken-link', body: '', unreadable: true },
					{
						name: 'locked',
						failAfter: 1,
						batches: [[{ name: 'first.txt', body: 'f' }], [{ name: 'never.txt', body: 'n' }]]
					}
				]
			]
		};

		const { files, skipped } = await readDirectory(fakeEntry(root) as FileSystemDirectoryEntry, '');

		expect(await summarize(files)).toEqual({ 'root/ok.txt': 'o', 'root/locked/first.txt': 'f' });
		expect(skipped).toBe(2);
	});
});

describe('readDrop', () => {
	it('mixes loose files with walked folders and reports empty folders', async () => {
		const items = fakeItems([
			{ name: 'loose.txt', body: 'L' },
			{ name: 'docs', batches: [[{ name: 'a.md', body: 'D' }]] },
			{ name: 'hollow', batches: [[{ name: 'inner', batches: [] }]] }
		]);

		const { files, emptyFolders, skipped } = await readDrop(items);

		expect(await summarize(files)).toEqual({ 'loose.txt': 'L', 'docs/a.md': 'D' });
		expect(emptyFolders).toEqual(['hollow']);
		expect(skipped).toBe(0);
	});

	it('keeps loose files when a dropped folder cannot be listed at all', async () => {
		const items = fakeItems([
			{ name: 'loose.txt', body: 'L' },
			{ name: 'denied', failAfter: 0, batches: [[{ name: 'x.txt', body: 'x' }]] }
		]);

		const { files, emptyFolders, skipped } = await readDrop(items);

		expect(await summarize(files)).toEqual({ 'loose.txt': 'L' });
		expect(emptyFolders).toEqual([]);
		expect(skipped).toBe(1);
	});
});
