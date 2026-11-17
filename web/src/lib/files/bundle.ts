import { Zip, ZipDeflate } from 'fflate';

export const BUNDLE_FILENAME = 'bundle.zip';

// One picked file and where it sits in the batch: a loose file's path is its
// name, a file from a folder keeps its folder-relative path ("photos/2024/a.jpg").
export type PickedFile = { file: File; path: string };

// A single loose file rides as-is (no reason to make the recipient unzip one
// file). Anything else is zipped — including a folder holding one file, whose
// folder structure is part of what the sender chose.
export function needsBundle(entries: PickedFile[]): boolean {
	return !(entries.length === 1 && !entries[0].path.includes('/'));
}

// Exactly one folder and nothing else names the zip after that folder;
// any other mix falls back to bundle.zip.
export function bundleName(entries: PickedFile[]): string {
	const roots = new Set(entries.map((entry) => entry.path.split('/')[0]));
	const onlyOneFolder = roots.size === 1 && entries.every((entry) => entry.path.includes('/'));
	return onlyOneFolder ? `${[...roots][0]}.zip` : BUNDLE_FILENAME;
}

// A secret carries exactly one encrypted payload, so several files can only ride
// along if they are first merged into one. needsBundle decides when a zip is
// built; the recipient downloads and extracts it. The zip is built before
// encryption, so the server still never sees any plaintext.
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
	// Each output chunk goes straight into a Blob, which the browser may keep on
	// disk, so neither the inputs nor the zip are ever whole in memory.
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
		// level 6 is the balanced default: it shrinks compressible inputs (text,
		// logs) while leaving already-compressed media effectively as-is.
		const entry = new ZipDeflate(uniqueName(path || 'file', usedNames), { level: 6 });
		archive.add(entry);
		const reader = file.stream().getReader();
		// Reading one chunk at a time keeps the form responsive between chunks.
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

// Two dropped files can share a path; a plain zip map would silently drop the
// first. Disambiguate collisions as "dir/name (1).ext" so every file survives.
// The extension is looked up in the last path segment only, so a dot in a
// folder name ("v1.2/readme") is never mistaken for one.
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
