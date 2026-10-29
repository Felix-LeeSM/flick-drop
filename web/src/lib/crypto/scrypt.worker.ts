// Runs the Model A scrypt step (work factor 18, about 256 MiB and a second or
// more on a phone) off the main thread so the page stays responsive. The
// passphrase stays in this tab: postMessage between a page and its own
// dedicated worker never leaves the browser.

import { unwrapIdentity, wrapIdentity } from './age';

export type ScryptRequest =
	| { op: 'wrap'; identity: string; passphrase: string }
	| { op: 'unwrap'; wrapped: Uint8Array; passphrase: string };

export type ScryptResponse = { ok: true; value: string | Uint8Array } | { ok: false };

self.onmessage = async (event: MessageEvent<ScryptRequest>) => {
	const request = event.data;
	try {
		const value =
			request.op === 'wrap'
				? await wrapIdentity(request.identity, request.passphrase)
				: await unwrapIdentity(request.wrapped, request.passphrase);
		self.postMessage({ ok: true, value } satisfies ScryptResponse);
	} catch {
		// No error text crosses back: it could only say "wrong passphrase".
		self.postMessage({ ok: false } satisfies ScryptResponse);
	}
};
