import { Zip, ZipDeflate } from 'fflate';

export const BUNDLE_FILENAME = 'bundle.zip';

// A secret carries exactly one encrypted payload, so several files can only ride
// along if they are first merged into one. A single file is passed through
// untouched (no reason to make the recipient unzip one file); two or more are
// packed into bundle.zip, which the recipient downloads and extracts. The zip is
// built before encryption, so the server still never sees any plaintext.
export async function bundleFiles(files: File[]): Promise<File> {
	if (files.length === 0) {
		throw new Error('at least one file is required');
	}
	if (files.length === 1) {
		return files[0];
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
	for (const file of files) {
		// level 6 is the balanced default: it shrinks compressible inputs (text,
		// logs) while leaving already-compressed media effectively as-is.
		const entry = new ZipDeflate(uniqueName(file.name || 'file', usedNames), { level: 6 });
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
	return new File(parts, BUNDLE_FILENAME, { type: 'application/zip' });
}

// Two dropped files can share a name; a plain zip map would silently drop the
// first. Disambiguate collisions as "name (1).ext" so every file survives.
function uniqueName(name: string, used: Set<string>): string {
	if (!used.has(name)) {
		used.add(name);
		return name;
	}
	const dot = name.lastIndexOf('.');
	const stem = dot > 0 ? name.slice(0, dot) : name;
	const ext = dot > 0 ? name.slice(dot) : '';
	let index = 1;
	let candidate = `${stem} (${index})${ext}`;
	while (used.has(candidate)) {
		index += 1;
		candidate = `${stem} (${index})${ext}`;
	}
	used.add(candidate);
	return candidate;
}
