import type { PickedFile } from './bundle';

export type DroppedBatch = {
	files: PickedFile[];
	// Dropped folders that held no file at any depth; they add nothing.
	emptyFolders: string[];
};

// Reads a drop into picked files, walking any dropped folder so each of its
// files keeps a folder-relative path. dataTransfer.files alone lists a folder
// as one opaque entry without its contents.
//
// The browser clears the DataTransferItemList once the drop event returns, so
// every item is captured before the first await — call this synchronously
// from the drop handler and await the result afterwards.
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
	for (const folder of folders) {
		const inside = await readDirectory(folder, '');
		if (inside.length === 0) {
			emptyFolders.push(folder.name);
		}
		files.push(...inside);
	}
	return { files, emptyFolders };
}

// Walks a directory depth-first. Empty subdirectories contribute nothing: a zip
// only carries files.
export async function readDirectory(
	directory: FileSystemDirectoryEntry,
	parentPath: string
): Promise<PickedFile[]> {
	const path = parentPath ? `${parentPath}/${directory.name}` : directory.name;
	const reader = directory.createReader();
	const files: PickedFile[] = [];
	// readEntries returns at most one batch per call (Chrome caps it at 100) and
	// signals the end with an empty batch, so keep reading until then.
	for (let batch = await readBatch(reader); batch.length > 0; batch = await readBatch(reader)) {
		for (const entry of batch) {
			if (entry.isDirectory) {
				files.push(...(await readDirectory(entry as FileSystemDirectoryEntry, path)));
			} else if (entry.isFile) {
				const file = await readFile(entry as FileSystemFileEntry);
				files.push({ file, path: `${path}/${entry.name}` });
			}
		}
	}
	return files;
}

function readBatch(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
	return new Promise((resolve, reject) => {
		reader.readEntries(resolve, reject);
	});
}

function readFile(entry: FileSystemFileEntry): Promise<File> {
	return new Promise((resolve, reject) => {
		entry.file(resolve, reject);
	});
}
