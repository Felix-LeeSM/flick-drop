import { describe, expect, it } from 'vitest';
import { readDirectory, readDrop } from './dropped';

// 브라우저 File System API(FileSystemDirectoryEntry 등)의 콜백 모양만 흉내 낸 가짜 객체다.
type FakeNode = FakeFile | FakeDirectory;
type FakeFile = { name: string; body: string };
type FakeDirectory = { name: string; batches: FakeNode[][] };

function fakeEntry(node: FakeNode): FileSystemEntry {
	if ('body' in node) {
		return {
			name: node.name,
			isFile: true,
			isDirectory: false,
			file: (resolve: (file: File) => void) => resolve(new File([node.body], node.name))
		} as unknown as FileSystemFileEntry;
	}
	return {
		name: node.name,
		isFile: false,
		isDirectory: true,
		createReader: () => {
			// readEntries는 한 번에 한 묶음만 주고, 빈 묶음으로 끝을 알린다.
			const pending = [...node.batches, []];
			return {
				readEntries: (resolve: (entries: FileSystemEntry[]) => void) =>
					resolve((pending.shift() ?? []).map(fakeEntry))
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
		const files = await readDirectory(fakeEntry(photos) as FileSystemDirectoryEntry, '');

		// Then: 모든 묶음의 파일이 폴더 구조를 담은 경로로 나온다
		expect(await summarize(files)).toEqual({
			'photos/a.jpg': 'A',
			'photos/b.jpg': 'B',
			'photos/2024/c.jpg': 'C'
		});
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

		const files = await readDirectory(fakeEntry(root) as FileSystemDirectoryEntry, '');

		expect(await summarize(files)).toEqual({ 'root/keep.txt': 'k' });
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
		const { files, emptyFolders } = await readDrop(items);

		// Then
		expect(await summarize(files)).toEqual({ 'loose.txt': 'L', 'docs/a.md': 'D' });
		expect(emptyFolders).toEqual(['hollow']);
	});
});
