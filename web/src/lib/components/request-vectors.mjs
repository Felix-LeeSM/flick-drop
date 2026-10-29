// biome-ignore-all lint/style/useNamingConvention: request fixtures use canonical API field names.
// Synthetic request-link material for the browser and live tests, generated
// per run with the same age library the app uses. age output is randomized, so
// there are no fixed bytes to pin; the tests check round trips instead.
import { createHash } from 'node:crypto';
import { Encrypter, generateHybridIdentity, identityToRecipient } from 'age-encryption';

async function seal(recipient, plaintext) {
	const encrypter = new Encrypter();
	encrypter.addRecipient(recipient);
	return Buffer.from(await encrypter.encrypt(plaintext)).toString('base64');
}

export async function requestVectors() {
	const identity = await generateHybridIdentity();
	const publicKey = await identityToRecipient(identity);
	const text = 'Synthetic request text';
	const file = { plaintext: 'Synthetic request file', filename: 'synthetic-é.txt' };
	const textBody = await seal(publicKey, text);
	const fileBody = await seal(publicKey, file.plaintext);
	return {
		request_id: 'synthetic-request',
		public_key: publicKey,
		fingerprint: createHash('sha256').update(publicKey).digest('base64url'),
		private_key: identity,
		text: {
			plaintext: text,
			payload: {
				kind: 'text',
				size_bytes: Buffer.from(textBody, 'base64').length,
				envelope: { version: 2 },
				ciphertext: textBody
			}
		},
		file: {
			...file,
			payload: {
				kind: 'file',
				size_bytes: Buffer.from(fileBody, 'base64').length,
				envelope: { version: 2, encrypted_filename: await seal(publicKey, file.filename) },
				ciphertext: fileBody
			}
		}
	};
}

// The open response the API sends for a submitted payload.
export function openResponse(payload) {
	const headers = {
		'Content-Type': 'application/octet-stream',
		// The app calls the API cross-origin; internal/httpapi/router.go exposes
		// these headers the same way.
		'Access-Control-Expose-Headers': 'Flick-Kind, Flick-Encrypted-Filename',
		'Flick-Kind': payload.kind
	};
	if (payload.envelope.encrypted_filename) {
		headers['Flick-Encrypted-Filename'] = payload.envelope.encrypted_filename;
	}
	return { headers, body: Buffer.from(payload.ciphertext, 'base64') };
}
