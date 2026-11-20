import { Zip, ZipDeflate } from 'fflate';

export const BUNDLE_FILENAME = 'bundle.zip';

export type PickedFile = { file: File; path: string };

// A folder holding one file still zips: its structure is part of what the sender chose.
export function needsBundle(entries: PickedFile[]): boolean {
	return !(entries.length === 1 && !entries[0].path.includes('/'));
}

export function bundleName(entries: PickedFile[]): string {
	const roots = new Set(entries.map((entry) => entry.path.split('/')[0]));
	const onlyOneFolder = roots.size === 1 && entries.every((entry) => entry.path.includes('/'));
	return onlyOneFolder ? `${[...roots][0]}.zip` : BUNDLE_FILENAME;
}

// Zipped before encryption, so the server never sees plaintext.
export async function bundleFiles(entries: PickedFile[]): Promise<File> {
	if (entries.length === 0) {
		throw new Error('at least one file is required');
	}
	if (!needsBundle(entries)) {
		return entries[0].file;
	}

	const parts: Blob[] = [];
	let failure: Error | null = null;
	let finished = false;
	// Chunks go straight into Blobs (may spill to disk), so nothing is whole in memory.
	const archive = new Zip((err, chunk, final) => {
		if (err) {
			failure = err;
			return;
		}
		parts.push(new Blob([chunk as Uint8Array<ArrayBuffer>]));
		finished = final;
	});
	const usedNames = new Set<string>();
	for (const { file, path } of entries) {
		// level 6: shrinks text, leaves already-compressed media as-is.
		const entry = new ZipDeflate(uniqueName(path || 'file', usedNames), { level: 6 });
		archive.add(entry);
		const reader = file.stream().getReader();
		for (let read = await reader.read(); !read.done; read = await reader.read()) {
			entry.push(read.value);
		}
		entry.push(new Uint8Array(0), true);
		if (failure) {
			throw failure;
		}
	}
	archive.end();
	if (failure || !finished) {
		throw failure ?? new Error('zip did not finish');
	}
	return new File(parts, bundleName(entries), { type: 'application/zip' });
}

// A zip map would drop duplicate paths; extension comes from the last segment only ("v1.2/readme").
function uniqueName(path: string, used: Set<string>): string {
	if (!used.has(path)) {
		used.add(path);
		return path;
	}
	const slash = path.lastIndexOf('/');
	const dot = path.lastIndexOf('.');
	const hasExt = dot > slash + 1;
	const stem = hasExt ? path.slice(0, dot) : path;
	const ext = hasExt ? path.slice(dot) : '';
	let index = 1;
	let candidate = `${stem} (${index})${ext}`;
	while (used.has(candidate)) {
		index += 1;
		candidate = `${stem} (${index})${ext}`;
	}
	used.add(candidate);
	return candidate;
}
