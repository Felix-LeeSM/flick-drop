import { describe, expect, it } from 'vitest';
import { readDirectory, readDrop } from './dropped';

// 브라우저 File System API(FileSystemDirectoryEntry 등)의 콜백 모양만 흉내 낸 가짜 객체다.
type FakeNode = FakeFile | FakeDirectory;
// unreadable: file()이 에러 콜백을 부른다. failAfter: 그만큼 묶음을 준 뒤 readEntries가 에러 콜백을 부른다.
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
			// readEntries는 한 번에 한 묶음만 주고, 빈 묶음으로 끝을 알린다.
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
		// Given: 두 묶음으로 나뉘어 읽히는 폴더와 그 안의 하위 폴더
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

		// When
		const { files, skipped } = await readDirectory(
			fakeEntry(photos) as FileSystemDirectoryEntry,
			''
		);

		// Then: 모든 묶음의 파일이 폴더 구조를 담은 경로로 나온다
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
		// Given: file()이 실패하는 파일 하나, 첫 묶음 뒤 readEntries가 실패하는 하위 폴더 하나
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

		// When
		const { files, skipped } = await readDirectory(fakeEntry(root) as FileSystemDirectoryEntry, '');

		// Then: 읽힌 파일은 남고, 실패한 두 항목이 건너뛴 수로 잡힌다
		expect(await summarize(files)).toEqual({ 'root/ok.txt': 'o', 'root/locked/first.txt': 'f' });
		expect(skipped).toBe(2);
	});
});

describe('readDrop', () => {
	it('mixes loose files with walked folders and reports empty folders', async () => {
		// Given: 낱개 파일, 내용이 있는 폴더, 빈 하위 폴더만 있는 폴더를 한 번에 놓는다
		const items = fakeItems([
			{ name: 'loose.txt', body: 'L' },
			{ name: 'docs', batches: [[{ name: 'a.md', body: 'D' }]] },
			{ name: 'hollow', batches: [[{ name: 'inner', batches: [] }]] }
		]);

		// When
		const { files, emptyFolders, skipped } = await readDrop(items);

		// Then
		expect(await summarize(files)).toEqual({ 'loose.txt': 'L', 'docs/a.md': 'D' });
		expect(emptyFolders).toEqual(['hollow']);
		expect(skipped).toBe(0);
	});

	it('keeps loose files when a dropped folder cannot be listed at all', async () => {
		// Given: 낱개 파일과, 첫 readEntries부터 실패하는 폴더
		const items = fakeItems([
			{ name: 'loose.txt', body: 'L' },
			{ name: 'denied', failAfter: 0, batches: [[{ name: 'x.txt', body: 'x' }]] }
		]);

		// When
		const { files, emptyFolders, skipped } = await readDrop(items);

		// Then: 드롭 전체가 버려지지 않고, 읽지 못한 폴더는 빈 폴더가 아니라 건너뛴 항목이다
		expect(await summarize(files)).toEqual({ 'loose.txt': 'L' });
		expect(emptyFolders).toEqual([]);
		expect(skipped).toBe(1);
	});
});
