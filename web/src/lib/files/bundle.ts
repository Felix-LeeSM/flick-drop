import { zip } from 'fflate';

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

	const entries: Record<string, Uint8Array> = {};
	const usedNames = new Set<string>();
	for (const file of files) {
		const name = uniqueName(file.name || 'file', usedNames);
		entries[name] = new Uint8Array(await file.arrayBuffer());
	}

	const zipped = await zipAsync(entries);
	// Widen to a plain ArrayBuffer: fflate types its output as Uint8Array<ArrayBufferLike>,
	// which BlobPart (ArrayBuffer-backed views only) rejects. The zip is never shared memory.
	const buffer = zipped.buffer.slice(
		zipped.byteOffset,
		zipped.byteOffset + zipped.byteLength
	) as ArrayBuffer;
	return new File([buffer], BUNDLE_FILENAME, { type: 'application/zip' });
}

// fflate's async zip runs off the main thread, so bundling a large batch does not
// freeze the create form. level 6 is the balanced default: it shrinks compressible
// inputs (text, logs) while leaving already-compressed media effectively as-is.
function zipAsync(entries: Record<string, Uint8Array>): Promise<Uint8Array> {
	return new Promise((resolve, reject) => {
		zip(entries, { level: 6 }, (err, data) => {
			if (err) {
				reject(err);
			} else {
				resolve(data);
			}
		});
	});
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
