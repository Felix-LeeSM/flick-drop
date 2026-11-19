import type { PickedFile } from './bundle';

export type DroppedBatch = {
	files: PickedFile[];
	emptyFolders: string[];
	skipped: number;
};

export type WalkedDirectory = { files: PickedFile[]; skipped: number };

// The browser clears DataTransferItemList once the drop event returns: call synchronously from the handler.
export function readDrop(items: DataTransferItemList): Promise<DroppedBatch> {
	const looseFiles: File[] = [];
	const folders: FileSystemDirectoryEntry[] = [];
	for (const item of Array.from(items)) {
		if (item.kind !== 'file') {
			continue;
		}
		const entry = item.webkitGetAsEntry?.() ?? null;
		if (entry?.isDirectory) {
			folders.push(entry as FileSystemDirectoryEntry);
			continue;
		}
		const file = item.getAsFile();
		if (file) {
			looseFiles.push(file);
		}
	}
	return collect(looseFiles, folders);
}

async function collect(
	looseFiles: File[],
	folders: FileSystemDirectoryEntry[]
): Promise<DroppedBatch> {
	const files: PickedFile[] = looseFiles.map((file) => ({ file, path: file.name }));
	const emptyFolders: string[] = [];
	let skipped = 0;
	for (const folder of folders) {
		const inside = await readDirectory(folder, '');
		skipped += inside.skipped;
		if (inside.files.length === 0 && inside.skipped === 0) {
			emptyFolders.push(folder.name);
		}
		files.push(...inside.files);
	}
	return { files, emptyFolders, skipped };
}

export async function readDirectory(
	directory: FileSystemDirectoryEntry,
	parentPath: string
): Promise<WalkedDirectory> {
	const path = parentPath ? `${parentPath}/${directory.name}` : directory.name;
	const reader = directory.createReader();
	const files: PickedFile[] = [];
	let skipped = 0;
	// readEntries returns one batch per call (Chrome caps at 100) until an empty batch.
	for (;;) {
		const batch = await readBatch(reader);
		if (batch === null) {
			skipped += 1;
			break;
		}
		if (batch.length === 0) {
			break;
		}
		for (const entry of batch) {
			const inside = await readEntry(entry, path);
			files.push(...inside.files);
			skipped += inside.skipped;
		}
	}
	return { files, skipped };
}

async function readEntry(entry: FileSystemEntry, parentPath: string): Promise<WalkedDirectory> {
	if (entry.isDirectory) {
		return readDirectory(entry as FileSystemDirectoryEntry, parentPath);
	}
	if (!entry.isFile) {
		return { files: [], skipped: 0 };
	}
	const file = await readFile(entry as FileSystemFileEntry);
	return file === null
		? { files: [], skipped: 1 }
		: { files: [{ file, path: `${parentPath}/${entry.name}` }], skipped: 0 };
}

// Resolve null on error so one unreadable entry is skipped, not fatal to the walk.
function readBatch(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[] | null> {
	return new Promise((resolve) => {
		reader.readEntries(resolve, () => resolve(null));
	});
}

function readFile(entry: FileSystemFileEntry): Promise<File | null> {
	return new Promise((resolve) => {
		entry.file(resolve, () => resolve(null));
	});
}
