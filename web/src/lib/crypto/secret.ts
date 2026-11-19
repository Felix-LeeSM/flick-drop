// The API client moves the bytes; this module never talks to the network.

import { decryptWith, encryptTo, unwrapIdentity, wrapIdentity } from './age';
import type { ScryptRequest, ScryptResponse } from './scrypt.worker';

export type SealedFile = {
	body: Blob;
	encryptedFilename: Uint8Array;
	contentType: string;
	// Size before encryption, which the client limits are expressed in.
	plaintextBytes: number;
};

export function sealText(identity: string, text: string): Promise<Uint8Array> {
	return encryptTo(identity, text);
}

// Encrypts while reading; the plaintext file is never loaded whole.
export async function sealFile(identity: string, file: File): Promise<SealedFile> {
	const encrypted = await encryptTo(identity, file.stream());
	return {
		body: await new Response(encrypted).blob(),
		encryptedFilename: await encryptTo(identity, file.name || 'flick-file'),
		contentType: file.type || 'application/octet-stream',
		plaintextBytes: file.size
	};
}

// Resolves only after the final chunk authenticates, so a truncated body rejects.
export async function openText(
	identity: string,
	body: ReadableStream<Uint8Array>
): Promise<string> {
	const plaintext = await new Response(await decryptWith(identity, body)).arrayBuffer();
	return new TextDecoder('utf-8', { fatal: true }).decode(plaintext);
}

// Nothing partial is downloadable. Always application/octet-stream: a text/html blob: URL
// would run in the Flick origin.
export async function openFile(
	identity: string,
	body: ReadableStream<Uint8Array>,
	encryptedFilename: Uint8Array
): Promise<{ blob: Blob; filename: string }> {
	const decrypted = await new Response(await decryptWith(identity, body)).blob();
	const filename = new TextDecoder('utf-8', { fatal: true }).decode(
		await decryptWith(identity, encryptedFilename)
	);
	return { blob: decrypted.slice(0, decrypted.size, 'application/octet-stream'), filename };
}

export async function wrapIdentityOffThread(
	identity: string,
	passphrase: string
): Promise<Uint8Array> {
	if (typeof Worker === 'undefined') {
		return wrapIdentity(identity, passphrase);
	}
	return (await runScrypt({ op: 'wrap', identity, passphrase })) as Uint8Array;
}

export async function unwrapIdentityOffThread(
	wrapped: Uint8Array,
	passphrase: string
): Promise<string> {
	if (typeof Worker === 'undefined') {
		return unwrapIdentity(wrapped, passphrase);
	}
	return (await runScrypt({ op: 'unwrap', wrapped, passphrase })) as string;
}

function runScrypt(request: ScryptRequest): Promise<string | Uint8Array> {
	const failure =
		request.op === 'wrap'
			? 'Could not protect the link with this passphrase.'
			: 'Could not unlock with this passphrase.';
	const worker = new Worker(new URL('./scrypt.worker.ts', import.meta.url), { type: 'module' });
	return new Promise<string | Uint8Array>((resolve, reject) => {
		worker.onmessage = (event: MessageEvent<ScryptResponse>) => {
			if (event.data.ok) {
				resolve(event.data.value);
			} else {
				reject(new Error(failure));
			}
		};
		worker.onerror = () => reject(new Error(failure));
		worker.onmessageerror = () => reject(new Error(failure));
		worker.postMessage(request);
	}).finally(() => worker.terminate());
}
